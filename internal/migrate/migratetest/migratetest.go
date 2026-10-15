// Package migratetest verifies migration fixtures, pure plans, and idempotence.
package migratetest

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/rogpeppe/go-internal/diff"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/migrate"
)

type options struct{ runner exec.Runner }

// Option configures a fixture run.
type Option func(*options)

// WithRunner permits subprocess calls through runner during the fixture run.
func WithRunner(runner exec.Runner) Option { return func(o *options) { o.runner = runner } }

// Run copies before to a temporary root and verifies Plan and Apply.
// Plan must preserve the tree. Apply must match after and remain idempotent.
// Every subprocess call fails the test unless WithRunner supplies a runner.
func Run(t testing.TB, m migrate.Migration, before, after string, opts ...Option) {
	t.Helper()
	o := options{runner: deniedRunner{t: t}}
	for _, opt := range opts {
		opt(&o)
	}
	if o.runner == nil {
		t.Fatal("migratetest: runner must not be nil")
	}
	if m.Plan == nil || m.Apply == nil {
		t.Fatal("migratetest: migration requires Plan and Apply")
	}
	root := t.TempDir()
	original := readTree(t, before)
	copyTree(t, root, original)
	initial := readTree(t, root)
	c := &migrate.Ctx{Root: root, From: "0.0.0", To: m.Since, Answers: map[string]any{}, Lock: &manifest.Lock{}, Runner: o.runner}
	if _, err := m.Plan(c); err != nil {
		t.Fatalf("migration %q Plan: %v", m.ID, err)
	}
	compareTrees(t, "Plan changed the tree", initial, readTree(t, root))
	if err := m.Apply(c); err != nil {
		t.Fatalf("migration %q Apply: %v", m.ID, err)
	}
	first := readTree(t, root)
	compareTrees(t, "Apply differs from after", readTree(t, after), first)
	if err := m.Apply(c); err != nil {
		t.Fatalf("migration %q second Apply: %v", m.ID, err)
	}
	compareTrees(t, "Apply is not idempotent", first, readTree(t, root))
}

type deniedRunner struct{ t testing.TB }

func (r deniedRunner) Run(_ context.Context, c exec.Cmd) (exec.Result, error) {
	r.t.Helper()
	r.t.Errorf("migratetest: unexpected subprocess: %s", c.String())
	return exec.Result{}, fmt.Errorf("unexpected subprocess: %s", c.String())
}

type entry struct {
	mode fs.FileMode
	data []byte
}
type tree map[string]entry

func readTree(t testing.TB, root string) tree {
	t.Helper()
	out := tree{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := entry{mode: info.Mode()}
		switch {
		case d.Type()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			e.data = []byte(target)
		case d.IsDir():
		case d.Type().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			e.data = data
		default:
			return fmt.Errorf("unsupported fixture file %q", path)
		}
		out[filepath.ToSlash(relative)] = e
		return nil
	})
	if err != nil {
		t.Fatalf("read fixture tree %s: %v", root, err)
	}
	return out
}

func copyTree(t testing.TB, root string, source tree) {
	t.Helper()
	paths := treePaths(source)
	for _, path := range paths {
		e := source[path]
		target := filepath.Join(root, filepath.FromSlash(path))
		// Directories remain writable until their contents have been copied.
		if e.mode.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if e.mode&os.ModeSymlink != 0 {
			if err := os.Symlink(string(e.data), target); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.WriteFile(target, e.data, e.mode.Perm()); err != nil {
				t.Fatal(err)
			}
		}
	}
	slices.Reverse(paths)
	for _, path := range paths {
		if source[path].mode.IsDir() {
			if err := os.Chmod(filepath.Join(root, filepath.FromSlash(path)), source[path].mode.Perm()); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func compareTrees(t testing.TB, label string, want, got tree) {
	t.Helper()
	all := tree{}
	for path := range want {
		all[path] = entry{}
	}
	for path := range got {
		all[path] = entry{}
	}
	for _, path := range treePaths(all) {
		w, wok := want[path]
		g, gok := got[path]
		switch {
		case !wok:
			t.Errorf("%s: unexpected path %s", label, path)
		case !gok:
			t.Errorf("%s: missing path %s", label, path)
		default:
			if w.mode != g.mode {
				t.Errorf("%s: %s mode: want %s, got %s", label, path, w.mode, g.mode)
			}
			if !bytes.Equal(w.data, g.data) {
				delta := diff.Diff("want/"+path, w.data, "got/"+path, g.data)
				if len(delta) == 0 {
					t.Errorf("%s: %s bytes: want %q, got %q", label, path, w.data, g.data)
				} else {
					t.Errorf("%s:\n%s", label, delta)
				}
			}
		}
	}
}
func treePaths(tree tree) []string {
	paths := make([]string, 0, len(tree))
	for path := range tree {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths
}
