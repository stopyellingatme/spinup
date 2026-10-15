package migrate_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/migrate"
	"github.com/stopyellingatme/spinup/internal/migrate/migratetest"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
)

const (
	kit2Config = "import adapter from '@sveltejs/adapter-node';\nexport default { kit: { adapter: adapter() } };\n"
	kit2Vite   = "import { sveltekit } from '@sveltejs/kit/vite';\nexport default { plugins: [sveltekit()] };\n"
	kit3Vite   = "import adapter from '@sveltejs/adapter-node';\nimport { sveltekit } from '@sveltejs/kit/vite';\nexport default { plugins: [sveltekit({ adapter: adapter() })] };\n"
	webPackage = "{\"name\": \"todo-web\"}\n"
)

// fakeSv imitates `sv migrate sveltekit-3`: it moves svelte.config.js into vite.config.js.
// Like the real tool, it changes nothing in a tree that has no Kit 2 code.
type fakeSv struct {
	mu    sync.Mutex
	calls []exec.Cmd
	err   error
}

func (f *fakeSv) Run(_ context.Context, c exec.Cmd) (exec.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if f.err != nil {
		return exec.Result{ExitCode: 1}, f.err
	}
	config := filepath.Join(c.Dir, "svelte.config.js")
	if _, err := os.Stat(config); errors.Is(err, os.ErrNotExist) {
		return exec.Result{}, nil
	}
	if err := os.WriteFile(filepath.Join(c.Dir, "vite.config.js"), []byte(kit3Vite), 0o644); err != nil {
		return exec.Result{}, err
	}
	return exec.Result{}, os.Remove(config)
}

func stack(kit string) *stackversions.Stack {
	return &stackversions.Stack{NPM: map[string]string{"@sveltejs/kit": kit, "sv": "1.1.1"}}
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, data := range files {
		target := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func kit2Tree(t *testing.T) string {
	return writeTree(t, map[string]string{
		"web/package.json":     webPackage,
		"web/svelte.config.js": kit2Config,
		"web/vite.config.js":   kit2Vite,
	})
}

func TestSvMigrateMajorBump(t *testing.T) {
	t.Parallel()
	before := kit2Tree(t)
	after := writeTree(t, map[string]string{"web/package.json": webPackage, "web/vite.config.js": kit3Vite})
	runner := &fakeSv{}
	m := migrate.SvMigrateWith(stack("2.49.0"), stack("3.0.1"))
	migratetest.Run(t, m, before, after, migratetest.WithRunner(runner))

	if len(runner.calls) != 2 {
		t.Fatalf("runner calls = %d, want 2 (Apply runs twice)", len(runner.calls))
	}
	for _, call := range runner.calls {
		want := []string{"dlx", "sv@1.1.1", "migrate", "sveltekit-3"}
		if call.Name != "pnpm" || !slices.Equal(call.Args, want) || filepath.Base(call.Dir) != "web" {
			t.Errorf("command = %s in %s, want pnpm %s in web/", call.String(), call.Dir, strings.Join(want, " "))
		}
	}
}

func TestSvMigrateNoop(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		old, new *stackversions.Stack
		files    map[string]string
	}{
		{"unknown old version", nil, stack("3.0.1"), nil},
		{"same major", stack("3.0.1"), stack("3.4.0"), nil},
		{"old version without web", &stackversions.Stack{NPM: map[string]string{}}, stack("3.0.1"), nil},
		{"api-only app", stack("2.49.0"), stack("3.0.1"), map[string]string{"api/go.mod": "module example.com/todo\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := tc.files
			if files == nil {
				files = map[string]string{"web/package.json": webPackage, "web/svelte.config.js": kit2Config, "web/vite.config.js": kit2Vite}
			}
			root := writeTree(t, files)
			// The harness fails the test on any subprocess call.
			migratetest.Run(t, migrate.SvMigrateWith(tc.old, tc.new), root, root)
		})
	}
}

func TestSvMigrateRegistered(t *testing.T) {
	t.Parallel()
	var found *migrate.Migration
	for _, m := range migrate.All() {
		if m.ID == migrate.SvMigrateID {
			found = &m
		}
	}
	if found == nil {
		t.Fatalf("migration %s is not registered", migrate.SvMigrateID)
	}
	if found.Stage != migrate.After || found.Since != "0.2.0" {
		t.Fatalf("stage %d since %s, want After since 0.2.0", found.Stage, found.Since)
	}
	// The harness leaves Ctx.Old nil, so the registered codemod plans and runs nothing.
	root := kit2Tree(t)
	migratetest.Run(t, *found, root, root)

	// With both versions in the context, it plans the sv run.
	c := &migrate.Ctx{
		Root: root, From: "0.1.0", To: "0.2.0", Answers: map[string]any{}, Lock: &manifest.Lock{},
		Old: &templates.Version{Version: "0.1.0", Stack: stack("2.49.0")},
		New: &templates.Version{Version: "0.2.0", Stack: stack("3.0.1")},
	}
	changes, err := found.Plan(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Path != "web" || !strings.Contains(changes[0].Desc, "pnpm dlx sv@1.1.1 migrate sveltekit-3") {
		t.Fatalf("changes = %+v, want one sv migrate run in web", changes)
	}
}

func TestSvMigrateErrors(t *testing.T) {
	t.Parallel()
	t.Run("runner failure", func(t *testing.T) {
		t.Parallel()
		root := kit2Tree(t)
		runner := &fakeSv{err: errors.New("exit status 1")}
		c := &migrate.Ctx{Root: root, Runner: runner}
		err := migrate.SvMigrateWith(stack("2.49.0"), stack("3.0.1")).Apply(c)
		if err == nil || !strings.Contains(err.Error(), "sv@1.1.1 migrate sveltekit-3") || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("err = %v, want the failed command and a Try line", err)
		}
	})
	t.Run("bad pin", func(t *testing.T) {
		t.Parallel()
		root := kit2Tree(t)
		_, err := migrate.SvMigrateWith(stack("next"), stack("3.0.1")).Plan(&migrate.Ctx{Root: root})
		if err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("err = %v, want a parse error with a Try line", err)
		}
	})
	t.Run("no sv pin", func(t *testing.T) {
		t.Parallel()
		root := kit2Tree(t)
		next := &stackversions.Stack{NPM: map[string]string{"@sveltejs/kit": "3.0.1"}}
		_, err := migrate.SvMigrateWith(stack("2.49.0"), next).Plan(&migrate.Ctx{Root: root})
		if err == nil || !strings.Contains(err.Error(), "sv pin") {
			t.Fatalf("err = %v, want a missing sv pin error", err)
		}
	})
}

func TestBunSvMigrate(t *testing.T) {
	t.Parallel()
	root := kit2Tree(t)
	var fake exec.Fake
	fake.Expect("bunx", "sv@1.1.1", "migrate", "sveltekit-3")
	m := migrate.SvMigrateWith(stack("2.49.0"), stack("3.0.1"))
	c := &migrate.Ctx{Root: root, Answers: map[string]any{"package_manager": "bun"}, Runner: &fake}
	changes, err := m.Plan(c)
	if err != nil || len(changes) != 1 || !strings.Contains(changes[0].Desc, "bunx sv@1.1.1") {
		t.Fatalf("plan = %v, %v", changes, err)
	}
	if err := m.Apply(c); err != nil {
		t.Fatal(err)
	}
	fake.AssertDone(t)
}
