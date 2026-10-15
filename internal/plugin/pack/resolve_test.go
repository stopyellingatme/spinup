package pack

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/plugin"
)

func TestApprovalPreservesRenderedLock(t *testing.T) {
	t.Parallel()
	m := manager(t)
	path := filepath.Join(m.Root, "spinup.lock")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := fixture(t)
	if err := m.Add(t.Context(), "path:"+source, ""); err != nil {
		t.Fatal(err)
	}
	replaceLine(t, source+"/plugin.toml", "version", "version = '2.0.0'")
	if err := m.Update(t.Context(), "example", "2.0.0"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("approval changed the rendered lock")
	}
	loaded, err := m.resolve(t.Context(), plugin.Options{Root: m.Root})
	if err != nil || len(loaded) != 1 || loaded[0].Meta.Version != "2.0.0" {
		t.Fatalf("declared plugins=%v error=%v", loaded, err)
	}
}

func recordRenderedPlugins(t *testing.T, m *Manager) {
	t.Helper()
	all, err := m.resolve(t.Context(), plugin.Options{Root: m.Root})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := manifest.Load(filepath.Join(m.Root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range all {
		lock.Plugins = append(lock.Plugins, manifest.Plugin{Name: p.Meta.Name, Version: p.Meta.Version, API: p.Meta.API, SHA256: p.SHA256})
	}
	if err := lock.Save(filepath.Join(m.Root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
}

func TestLockedPluginUsesHistoryAfterRemoval(t *testing.T) {
	t.Parallel()
	m := manager(t)
	source := fixture(t)
	if err := m.Add(t.Context(), "path:"+source, ""); err != nil {
		t.Fatal(err)
	}
	recordRenderedPlugins(t, m)
	if err := m.Remove(t.Context(), "example"); err != nil {
		t.Fatal(err)
	}
	loaded, err := m.resolve(t.Context(), plugin.Options{Root: m.Root, Offline: true, Versions: map[string]string{"example": "1.0.0"}})
	if err != nil || len(loaded) != 1 {
		t.Fatalf("OLD=%v error=%v", loaded, err)
	}
	if err := os.Remove(filepath.Join(m.Root, historyFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.resolve(t.Context(), plugin.Options{Root: m.Root, Offline: true, Versions: map[string]string{"example": "1.0.0"}}); err == nil {
		t.Fatal("accepted missing history")
	}
}

func TestLockedHashSelectsSameVersionHistory(t *testing.T) {
	t.Parallel()
	m := manager(t)
	source := fixture(t)
	if err := m.Add(t.Context(), "path:"+source, ""); err != nil {
		t.Fatal(err)
	}
	old, err := m.resolve(t.Context(), plugin.Options{Root: m.Root})
	if err != nil {
		t.Fatal(err)
	}
	recordRenderedPlugins(t, m)
	if err := os.WriteFile(filepath.Join(source, "files/example.txt.tmpl"), []byte("new bytes\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := m.Update(t.Context(), "example", ""); err != nil {
		t.Fatal(err)
	}
	loaded, err := m.resolve(t.Context(), plugin.Options{Root: m.Root, Offline: true, Versions: map[string]string{"example": "1.0.0"}})
	if err != nil || len(loaded) != 1 || loaded[0].SHA256 != old[0].SHA256 {
		t.Fatalf("OLD=%v error=%v", loaded, err)
	}
}

func TestResolveLockedIntermediateHash(t *testing.T) {
	t.Parallel()
	m := manager(t)
	source := fixture(t)
	if err := m.Add(t.Context(), "path:"+source, ""); err != nil {
		t.Fatal(err)
	}
	recordRenderedPlugins(t, m)
	if err := os.WriteFile(filepath.Join(source, "files/example.txt.tmpl"), []byte("new bytes\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := m.Update(t.Context(), "example", ""); err != nil {
		t.Fatal(err)
	}
	newer, err := m.resolve(t.Context(), plugin.Options{Root: m.Root})
	if err != nil {
		t.Fatal(err)
	}
	expected := manifest.Plugin{Name: "example", Version: "1.0.0", API: plugin.API, SHA256: newer[0].SHA256}
	// A chained upgrade has advanced its in-memory lock, while the checkout keeps OLD.
	loaded, err := m.resolveLocked(t.Context(), plugin.Options{Root: m.Root, Offline: true}, []manifest.Plugin{expected})
	if err != nil || len(loaded) != 1 || loaded[0].SHA256 != expected.SHA256 {
		t.Fatalf("intermediate OLD=%v error=%v", loaded, err)
	}
}
