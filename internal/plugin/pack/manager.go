package pack

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	execx "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/project"
)

// Manager manages plugin declarations and trust records in one project.
type Manager struct {
	// Root is the project directory.
	Root string
	// PathRoot resolves local sources while staging a new project. Empty uses Root.
	PathRoot string
	// CacheDir overrides the user cache directory for tests or isolated callers.
	CacheDir string
	// CLIVersion is the build version used to check the spinup range.
	CLIVersion string
	// Runner executes git. Nil selects the operating system runner.
	Runner execx.Runner
	// Offline requires retained cache entries.
	Offline bool
	// Trust supplies explicit approval instead of a terminal confirmation.
	Trust bool
	// Interactive permits a terminal confirmation.
	Interactive bool
	// DryRun previews changes without writing records or caches.
	DryRun bool
	// In supplies terminal confirmation input.
	In io.Reader
	// Out receives metadata, file lists, and progress.
	Out io.Writer
}

func (m *Manager) output() io.Writer {
	if m.Out == nil {
		return io.Discard
	}
	return m.Out
}
func (m *Manager) state() (*project.Config, *manifest.Lock, error) {
	cfg, err := project.Load(m.Root)
	if err != nil {
		return nil, nil, problem("load plugin project", err)
	}
	lock, err := manifest.Load(filepath.Join(m.Root, project.LockFile))
	return cfg, lock, err
}
func (m *Manager) confirm() error {
	if m.Trust {
		return nil
	}
	if !m.Interactive || m.In == nil {
		return fmt.Errorf("cannot trust plugin without a terminal\nTry: inspect the plugin, then pass --trust")
	}
	if _, err := fmt.Fprint(m.output(), "Trust this plugin? Type yes: "); err != nil {
		return problem("write plugin confirmation", err)
	}
	answer, err := bufio.NewReader(m.In).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return problem("read plugin confirmation", err)
	}
	if strings.TrimSpace(answer) != "yes" {
		return fmt.Errorf("plugin trust was not confirmed\nTry: inspect the plugin before retrying")
	}
	return nil
}
func (m *Manager) prepare(ctx context.Context, source, version, name string) (tree, plugin.Loaded, error) {
	tr, err := m.fetch(ctx, source, version, name)
	if err != nil {
		return tr, plugin.Loaded{}, err
	}
	meta, err := validate(tr, m.CLIVersion)
	if err != nil {
		return tr, plugin.Loaded{}, err
	}
	if name != "" && meta.Name != name {
		return tr, plugin.Loaded{}, problem("validate plugin identity", fmt.Errorf("got %q, expected %q", meta.Name, name))
	}
	if version != "" && !sameVersion(meta.Version, version) {
		return tr, plugin.Loaded{}, problem("validate plugin version", fmt.Errorf("metadata %q differs from tag %q", meta.Version, version))
	}
	return tr, plugin.Loaded{Meta: meta, Source: source, SHA256: tr.hash(), FS: tr.fs()}, nil
}
func (m *Manager) show(loaded plugin.Loaded) error {
	p, err := loadPack(loaded.FS, loaded.Meta.Name)
	if err != nil {
		return err
	}
	out := m.output()
	_, err = fmt.Fprintf(out, "Name: %s\nVersion: %s\nAPI: %d\nSpinup: %s\nSource: %s\nSHA256: %s\nDescription: %s\nFiles:\n", loaded.Meta.Name, loaded.Meta.Version, loaded.Meta.API, loaded.Meta.Spinup, loaded.Source, loaded.SHA256, loaded.Meta.Description)
	if err != nil {
		return problem("write plugin metadata", err)
	}
	for _, file := range p.Files {
		if _, err := fmt.Fprintf(out, "  %s (%s)\n", file.Path, file.Class); err != nil {
			return problem("write plugin files", err)
		}
	}
	for _, g := range loaded.Meta.Generators {
		if _, err := fmt.Fprintf(out, "Generator: %s %s\n  Usage: %s\n  Args: %s\n", g.Name, g.Short, g.Usage, strings.Join(g.Args, ", ")); err != nil {
			return problem("write generator metadata", err)
		}
		for _, file := range g.Files {
			if _, err := fmt.Fprintf(out, "  %s (%s)\n", file.Path, file.Class); err != nil {
				return problem("write generator files", err)
			}
		}
	}
	return nil
}

// Add validates a source and records explicit trust without rendering its files.
func (m *Manager) Add(ctx context.Context, source, version string) error {
	cfg, lock, err := m.state()
	if err != nil {
		return err
	}
	tr, loaded, err := m.prepare(ctx, source, version, "")
	if err != nil {
		return err
	}
	for _, entry := range cfg.Plugins {
		if entry.Name == loaded.Meta.Name {
			return problem("add plugin", fmt.Errorf("plugin %q already exists", entry.Name))
		}
	}
	if err := m.show(loaded); err != nil {
		return err
	}
	if !m.DryRun {
		if err := m.confirm(); err != nil {
			return err
		}
		if err := m.cache(tr, loaded); err != nil {
			return err
		}
	}
	if version == "" {
		version = loaded.Meta.Version
	}
	if err := m.writeRecords(cfg, lock, project.Plugin{Name: loaded.Meta.Name, Source: source, Version: version}, &loaded); err != nil {
		return err
	}
	_, err = fmt.Fprintln(m.output(), "Next: spinup upgrade --plan")
	if err != nil {
		return problem("write plugin next step", err)
	}
	return m.warnIgnoredRecords(ctx)
}

// Update validates a new version and replaces its declaration and trust records.
func (m *Manager) Update(ctx context.Context, name, version string) error {
	cfg, lock, err := m.state()
	if err != nil {
		return err
	}
	for _, decl := range cfg.Plugins {
		if decl.Name != name {
			continue
		}
		if version == "" {
			version = decl.Version
		}
		tr, loaded, err := m.prepare(ctx, decl.Source, version, name)
		if err != nil {
			return err
		}
		oldHash := "(missing)"
		allow, err := readAllowlist(m.Root, true)
		if err != nil {
			return err
		}
		if approved, ok := allow[name]; ok {
			oldHash = approved.SHA256
		}
		if _, err := fmt.Fprintf(m.output(), "Hash: %s -> %s\n", oldHash, loaded.SHA256); err != nil {
			return problem("write hash change", err)
		}
		if err := m.show(loaded); err != nil {
			return err
		}
		if !m.DryRun {
			if err := m.confirm(); err != nil {
				return err
			}
			if err := m.cache(tr, loaded); err != nil {
				return err
			}
		}
		decl.Version = version
		if err := m.writeRecords(cfg, lock, decl, &loaded); err != nil {
			return err
		}
		_, err = fmt.Fprintln(m.output(), "Next: spinup upgrade --plan")
		if err != nil {
			return problem("write plugin next step", err)
		}
		return m.warnIgnoredRecords(ctx)
	}
	return problem("update plugin", fmt.Errorf("plugin %q is not declared", name))
}

// Remove deletes the declaration and current approval without changing rendered files.
// The next upgrade deletes unmodified pack files and preserves edited files as orphans.
func (m *Manager) Remove(_ context.Context, name string) error {
	cfg, lock, err := m.state()
	if err != nil {
		return err
	}
	for _, decl := range cfg.Plugins {
		if decl.Name != name {
			continue
		}
		var owned []string
		for path, entry := range lock.Files {
			if strings.HasPrefix(entry.Template, "plugin/"+name+"/") {
				owned = append(owned, path)
			}
		}
		sort.Strings(owned)
		if err := m.writeRecords(cfg, lock, decl, nil); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(m.output(), "Removed plugin %s. The next upgrade deletes unmodified pack files and preserves edited files as orphans.\nRetained files until the next upgrade (review with spinup upgrade --plan):\n", name); err != nil {
			return problem("write plugin removal", err)
		}
		for _, path := range owned {
			if _, err := fmt.Fprintln(m.output(), "  "+path); err != nil {
				return problem("write affected plugin files", err)
			}
		}
		return nil
	}
	return problem("remove plugin", fmt.Errorf("plugin %q is not declared", name))
}

// Entry describes a declared plugin and its lock state.
type Entry struct {
	// Name identifies the plugin.
	Name string `json:"name"`
	// Version is the declared version or tag.
	Version string `json:"version"`
	// Source is the declared path or git URL.
	Source string `json:"source"`
	// SHA256 is the locked tree hash.
	SHA256 string `json:"sha256"`
	// API is the locked plugin API.
	API int `json:"api"`
}

// List returns declarations in project order, including their locked hashes.
func (m *Manager) List() ([]Entry, error) {
	cfg, lock, err := m.state()
	if err != nil {
		return nil, err
	}
	result := make([]Entry, 0, len(cfg.Plugins))
	for _, decl := range cfg.Plugins {
		entry := Entry{Name: decl.Name, Version: decl.Version, Source: decl.Source}
		for _, locked := range lock.Plugins {
			if locked.Name == decl.Name {
				entry.SHA256 = locked.SHA256
				entry.API = locked.API
			}
		}
		result = append(result, entry)
	}
	return result, nil
}

// Info prints verified metadata, generators, and files for one plugin.
func (m *Manager) Info(ctx context.Context, name string) error {
	all, err := m.resolve(ctx, plugin.Options{Root: m.Root, Offline: m.Offline})
	if err != nil {
		return err
	}
	for _, loaded := range all {
		if loaded.Meta.Name == name {
			return m.show(loaded)
		}
	}
	return problem("inspect plugin", fmt.Errorf("plugin %q is not declared", name))
}

// Verify re-fetches sources or re-hashes offline caches and checks trust records.
func (m *Manager) Verify(ctx context.Context) error {
	all, err := m.resolveAll(ctx, plugin.Options{Root: m.Root, Offline: m.Offline}, true)
	if err != nil {
		return err
	}
	for _, loaded := range all {
		if _, err := fmt.Fprintf(m.output(), "Verified %s %s %s\n", loaded.Meta.Name, loaded.Meta.Version, loaded.SHA256); err != nil {
			return problem("write plugin verification", err)
		}
	}
	return nil
}

// warnIgnoredRecords reports legacy ignore rules without requiring a Git repository.
func (m *Manager) warnIgnoredRecords(ctx context.Context) error {
	runner := m.Runner
	if runner == nil {
		runner = execx.OSRunner{}
	}
	result, _ := runner.Run(ctx, execx.Cmd{Name: "git", Args: []string{"check-ignore", "--", plugin.AllowlistFile, ".spinup/plugins.history"}, Dir: m.Root})
	if len(result.Stdout) == 0 {
		return nil
	}
	_, err := fmt.Fprintln(m.output(), "Warning: Git ignores plugin trust records.\nTry: replace .spinup/ in .gitignore with these lines:\n.spinup/*\n!.spinup/plugins.allow\n!.spinup/plugins.history")
	if err != nil {
		return problem("write ignored plugin records warning", err)
	}
	return nil
}
