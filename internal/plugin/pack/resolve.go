package pack

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"runtime/debug"
	"slices"
	"strings"
	"sync"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/project"
)

var versionMu sync.RWMutex
var versionProvider = func() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

// SetVersionProvider supplies the CLI build version without importing CLI wiring.
// Call it during command registration.
func SetVersionProvider(provider func() string) {
	versionMu.Lock()
	defer versionMu.Unlock()
	versionProvider = provider
}
func buildVersion() string { versionMu.RLock(); defer versionMu.RUnlock(); return versionProvider() }
func init()                { plugin.SetResolver(resolve) }
func resolve(ctx context.Context, o plugin.Options) ([]plugin.Loaded, error) {
	return (&Manager{Root: o.Root, Offline: o.Offline, Out: o.Out, CLIVersion: buildVersion()}).resolve(ctx, o)
}
func (m *Manager) resolve(ctx context.Context, o plugin.Options) ([]plugin.Loaded, error) {
	return m.resolveAll(ctx, o, false)
}
func (m *Manager) resolveAll(ctx context.Context, o plugin.Options, refresh bool) ([]plugin.Loaded, error) {
	cfg, err := project.Load(m.Root)
	if err != nil {
		return nil, problem("load plugin declarations", err)
	}
	inputs, err := m.resolveInputs(cfg, o.Versions)
	if err != nil {
		return nil, err
	}
	return m.loadInputs(ctx, o, inputs, refresh)
}

func (m *Manager) loadInputs(ctx context.Context, o plugin.Options, inputs []resolveInput, refresh bool) ([]plugin.Loaded, error) {
	var err error
	var result []plugin.Loaded
	for _, input := range inputs {
		decl, expectedHash := input.Plugin, input.hash
		var tr tree
		useCache := m.Offline || o.Offline || input.historical || (!refresh && strings.HasPrefix(decl.Source, "git:"))
		if useCache {
			tr, err = m.cached(decl.Name, decl.Version, decl.Source, expectedHash)
		}
		if !useCache || errors.Is(err, fs.ErrNotExist) && !m.Offline && !o.Offline && !input.historical {
			tr, err = m.fetch(ctx, decl.Source, decl.Version, decl.Name)
		}
		if err != nil {
			return nil, err
		}
		meta, err := validate(tr, m.CLIVersion)
		if err != nil {
			return nil, err
		}
		if meta.Name != decl.Name || !sameVersion(meta.Version, decl.Version) {
			return nil, problem("resolve plugin", fmt.Errorf("metadata differs from declaration for %q", decl.Name))
		}
		hash := tr.hash()
		if hash != expectedHash {
			return nil, problem("resolve plugin", fmt.Errorf("plugin %q hash mismatch: got %s, approved %s", decl.Name, hash, expectedHash))
		}
		// Preserve the declared tag spelling in the lock without changing tree bytes.
		meta.Version = decl.Version
		loaded := plugin.Loaded{Meta: meta, Source: decl.Source, SHA256: hash, FS: tr.fs()}
		if !m.Offline && !o.Offline && !input.historical {
			if err := m.cache(tr, loaded); err != nil {
				return nil, err
			}
		}
		result = append(result, loaded)
	}
	return result, nil
}

type resolveInput struct {
	project.Plugin
	hash       string
	historical bool
}

// resolveInputs separates current approval from the inputs of a historical render.
// Overrides select only the named plugins, including removed declarations retained in history.
func (m *Manager) resolveInputs(cfg *project.Config, versions map[string]string) ([]resolveInput, error) {
	declared := map[string]project.Plugin{}
	for _, decl := range cfg.Plugins {
		if _, duplicate := declared[decl.Name]; duplicate || !namePattern.MatchString(decl.Name) || !safeVersion(decl.Version) {
			return nil, problem("resolve plugin", fmt.Errorf("invalid or duplicate declaration %q", decl.Name))
		}
		declared[decl.Name] = decl
	}
	if len(versions) == 0 {
		if len(cfg.Plugins) == 0 {
			return nil, nil
		}
		allow, err := readAllowlist(m.Root, false)
		if err != nil {
			return nil, err
		}
		var inputs []resolveInput
		for _, decl := range cfg.Plugins {
			approved, ok := allow[decl.Name]
			if !ok || approved.Source != decl.Source {
				return nil, problem("resolve plugin", fmt.Errorf("plugin %q differs from the allowlist", decl.Name))
			}
			inputs = append(inputs, resolveInput{Plugin: decl, hash: approved.SHA256})
		}
		return inputs, nil
	}
	lock, err := manifest.Load(m.Root + "/" + project.LockFile)
	if err != nil {
		return nil, err
	}
	locked := map[string]manifest.Plugin{}
	for _, entry := range lock.Plugins {
		if _, duplicate := locked[entry.Name]; duplicate || entry.API != plugin.API || !validHash(entry.SHA256) {
			return nil, problem("resolve plugins", fmt.Errorf("invalid or duplicate lock entry %q", entry.Name))
		}
		locked[entry.Name] = entry
	}
	history, err := readHistory(m.Root)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(versions))
	for name := range versions {
		names = append(names, name)
	}
	slices.Sort(names)
	var inputs []resolveInput
	for _, name := range names {
		version := versions[name]
		if !namePattern.MatchString(name) || !safeVersion(version) {
			return nil, problem("resolve historical plugin", fmt.Errorf("invalid identity %s@%s", name, version))
		}
		entry := locked[name]
		decl := declared[name]
		if sameVersion(entry.Version, version) {
			input, err := lockedInput(entry, history)
			if err != nil {
				return nil, err
			}
			input.Version = version
			inputs = append(inputs, input)
			continue
		}
		source := decl.Source
		hash, err := m.historyHash(name, version, source)
		if err != nil {
			return nil, err
		}
		if hash == "" {
			return nil, problem("resolve historical plugin", fmt.Errorf("project history has no matching approved hash for %s@%s", name, version))
		}
		inputs = append(inputs, resolveInput{Plugin: project.Plugin{Name: name, Source: source, Version: version}, hash: hash, historical: true})
	}
	return inputs, nil
}

// ResolveLocked loads exactly the rendered plugins from a lock, using project history and cached bytes.
// The supplied entries can come from an intermediate lock during a chained upgrade.
func ResolveLocked(ctx context.Context, o plugin.Options, entries []manifest.Plugin) ([]plugin.Loaded, error) {
	m := &Manager{Root: o.Root, Offline: true, Out: o.Out, CLIVersion: buildVersion()}
	return m.resolveLocked(ctx, o, entries)
}

func (m *Manager) resolveLocked(ctx context.Context, o plugin.Options, entries []manifest.Plugin) ([]plugin.Loaded, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	history, err := readHistory(m.Root)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	inputs := make([]resolveInput, 0, len(entries))
	for _, entry := range entries {
		if seen[entry.Name] {
			return nil, problem("resolve historical plugin", fmt.Errorf("duplicate locked plugin %q", entry.Name))
		}
		seen[entry.Name] = true
		input, err := lockedInput(entry, history)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, input)
	}
	return m.loadInputs(ctx, o, inputs, false)
}

func lockedInput(entry manifest.Plugin, history []historyEntry) (resolveInput, error) {
	if !namePattern.MatchString(entry.Name) || !safeVersion(entry.Version) || entry.API != plugin.API || !validHash(entry.SHA256) {
		return resolveInput{}, problem("resolve historical plugin", fmt.Errorf("invalid lock entry for %q", entry.Name))
	}
	var source string
	for _, h := range history {
		if h.Name != entry.Name || !sameVersion(h.Version, entry.Version) || h.SHA256 != entry.SHA256 {
			continue
		}
		if source != "" && source != h.Source {
			return resolveInput{}, problem("resolve historical plugin", fmt.Errorf("project history has multiple sources for %s@%s", entry.Name, entry.Version))
		}
		source = h.Source
	}
	if source == "" {
		return resolveInput{}, problem("resolve historical plugin", fmt.Errorf("project history has no matching approved hash for %s@%s", entry.Name, entry.Version))
	}
	return resolveInput{Plugin: project.Plugin{Name: entry.Name, Source: source, Version: entry.Version}, hash: entry.SHA256, historical: true}, nil
}

func sameVersion(a, b string) bool { return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v") }
