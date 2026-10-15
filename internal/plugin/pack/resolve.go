package pack

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"runtime/debug"
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
	if len(cfg.Plugins) == 0 {
		return nil, nil
	}
	lock, err := manifest.Load(m.Root + "/" + project.LockFile)
	if err != nil {
		return nil, err
	}
	allow, err := readAllowlist(m.Root, false)
	if err != nil {
		return nil, err
	}
	locked := map[string]manifest.Plugin{}
	for _, entry := range lock.Plugins {
		if _, ok := locked[entry.Name]; ok {
			return nil, problem("resolve plugins", fmt.Errorf("duplicate lock entry %q", entry.Name))
		}
		locked[entry.Name] = entry
	}
	seen := map[string]bool{}
	var result []plugin.Loaded
	for _, decl := range cfg.Plugins {
		if seen[decl.Name] || !namePattern.MatchString(decl.Name) || !safeVersion(decl.Version) {
			return nil, problem("resolve plugin", fmt.Errorf("invalid or duplicate declaration %q", decl.Name))
		}
		seen[decl.Name] = true
		entry, ok := locked[decl.Name]
		approved, trusted := allow[decl.Name]
		if !ok || !trusted || entry.API != plugin.API || approved.Source != decl.Source || approved.SHA256 != entry.SHA256 || !validHash(entry.SHA256) {
			return nil, problem("resolve plugin", fmt.Errorf("plugin %q differs from the lock or allowlist", decl.Name))
		}
		version := decl.Version
		if override, ok := o.Versions[decl.Name]; ok {
			version = override
		}
		if !safeVersion(version) {
			return nil, problem("resolve plugin", fmt.Errorf("invalid version %q", version))
		}
		var tr tree
		// Historical overrides require a hash approved in this project's history.
		historical := !sameVersion(version, entry.Version)
		if historical && o.Versions[decl.Name] == "" {
			return nil, problem("resolve plugin", fmt.Errorf("plugin %q version differs from lock", decl.Name))
		}
		expectedHash := entry.SHA256
		if _, overridden := o.Versions[decl.Name]; overridden {
			historyHash, historyErr := m.historyHash(decl.Name, version, decl.Source)
			if historyErr != nil {
				return nil, historyErr
			}
			if historical && historyHash == "" {
				return nil, problem("resolve historical plugin", fmt.Errorf("project history has no approved hash for %s@%s", decl.Name, version))
			}
			if historical {
				expectedHash = historyHash
			} else if historyHash != "" && historyHash != expectedHash {
				return nil, problem("resolve historical plugin", fmt.Errorf("project history differs from the lock for %s@%s", decl.Name, version))
			}
		}
		useCache := m.Offline || o.Offline || historical || (!refresh && strings.HasPrefix(decl.Source, "git:"))
		if useCache {
			tr, err = m.cached(decl.Name, version, decl.Source, expectedHash)
		}
		if !useCache || errors.Is(err, fs.ErrNotExist) && !m.Offline && !o.Offline && !historical {
			tr, err = m.fetch(ctx, decl.Source, version, decl.Name)
		}
		if err != nil {
			return nil, err
		}
		meta, err := validate(tr, m.CLIVersion)
		if err != nil {
			return nil, err
		}
		if meta.Name != decl.Name || !sameVersion(meta.Version, version) {
			return nil, problem("resolve plugin", fmt.Errorf("metadata differs from declaration for %q", decl.Name))
		}
		hash := tr.hash()
		if hash != expectedHash {
			return nil, problem("resolve plugin", fmt.Errorf("plugin %q hash mismatch: got %s, approved %s", decl.Name, hash, expectedHash))
		}
		loaded := plugin.Loaded{Meta: meta, Source: decl.Source, SHA256: hash, FS: tr.fs()}
		if !m.Offline && !o.Offline && !historical {
			if err := m.cache(tr, loaded); err != nil {
				return nil, err
			}
		}
		result = append(result, loaded)
	}
	return result, nil
}
func sameVersion(a, b string) bool { return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v") }
