package destroy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen/fields"
	"github.com/stopyellingatme/spinup/internal/gen/resource"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func TestRun(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	lock := manifest.New()
	name, _ := fields.ParseName("todo")
	paths := append(resource.Paths(name), "api/db/migrations/00002_create_todos.sql", "api/db/kit/00002_create_auth.sql")
	for _, path := range paths {
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("original"), 0644); err != nil {
			t.Fatal(err)
		}
		lock.Files[path] = manifest.FileEntry{Class: manifest.Scaffold, Template: "gen/resource", Hash: manifest.Hash([]byte("original")), Mode: "0644"}
	}
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	changed := "api/internal/handlers/todos.go"
	if err := os.WriteFile(filepath.Join(root, changed), []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	fake := &exec.Fake{}
	if err := Run(context.Background(), Options{Root: root, Kind: "resource", Name: "todo", DryRun: true, Out: &out}, fake); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "would remove web/src/routes/todos/\n") {
		t.Fatalf("dry run lists no emptied directory:\n%s", out.String())
	}
	for _, path := range paths {
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			t.Fatal(err)
		}
	}
	// The bundle step can run without invoking oapi-codegen when no paths remain.
	if err := os.MkdirAll(filepath.Join(root, "api/openapi"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api/openapi/openapi.yaml"), []byte("openapi: 3.0.3\ninfo:\n  title: Test\n  version: 1.0.0\npaths: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(context.Background(), Options{Root: root, Kind: "resource", Name: "todo", Out: &out}, fake); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "keep "+changed) || !strings.Contains(out.String(), "Next: spinup db new drop_todos") {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(filepath.Join(root, "web/src/routes/todos")); !os.IsNotExist(err) {
		t.Fatalf("destroy left web/src/routes/todos: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "api/internal/handlers")); err != nil {
		t.Fatalf("destroy removed the folder of a kept file: %v", err)
	}
	updated, err := manifest.Load(filepath.Join(root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		_, tracked := updated.Files[path]
		_, err := os.Stat(filepath.Join(root, path))
		keep := path == changed || (strings.Contains(path, "/migrations/") || strings.Contains(path, "/kit/"))
		if tracked != keep || (err == nil) != keep {
			t.Fatalf("%s: tracked=%v error=%v", path, tracked, err)
		}
	}
	fake.AssertDone(t)
}

func TestBuildKinds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ kind, name, path string }{
		{"model", "todo", "api/db/queries/todos.sql"},
		{"handler", "healthReports", "api/internal/handlers/health_reports.go"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			write := func(path, data string) {
				t.Helper()
				p := filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(data), 0644); err != nil {
					t.Fatal(err)
				}
			}
			write("api/go.mod", "module example.com/test/api\n")
			write(tc.path, "original")
			lock := manifest.New()
			for _, scenario := range []string{"untracked", "changed", "matching", "symlink"} {
				if scenario != "untracked" {
					lock.Files[tc.path] = manifest.FileEntry{Class: manifest.Scaffold, Template: "gen/" + tc.kind, Hash: manifest.Hash([]byte("original")), Mode: "0644"}
				}
				if scenario == "changed" {
					write(tc.path, "changed")
				}
				if scenario == "matching" {
					write(tc.path, "original")
				}
				if scenario == "symlink" {
					outside := filepath.Join(t.TempDir(), "outside")
					if err := os.WriteFile(outside, []byte("original"), 0644); err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(filepath.Join(root, tc.path)); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(outside, filepath.Join(root, tc.path)); err != nil {
						t.Fatal(err)
					}
				}
				plan, err := Build(context.Background(), Options{Root: root, Kind: tc.kind, Name: tc.name}, lock)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "matching" {
					if len(plan.Files.Files) != 1 || len(plan.Kept) != 0 {
						t.Fatalf("%s: %+v", scenario, plan)
					}
				} else if len(plan.Files.Files) != 0 || len(plan.Kept) != 1 {
					t.Fatalf("%s: %+v", scenario, plan)
				}
			}
		})
	}
}

func TestBuildRebuildsNav(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	name, err := fields.ParseName("todo")
	if err != nil {
		t.Fatal(err)
	}
	lock := manifest.New()
	lock.Packs = []string{"base", "api", "web"}
	write := func(path, data string, class manifest.Class, template string) {
		t.Helper()
		filename := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		lock.Files[path] = manifest.FileEntry{Class: class, Template: template, Hash: manifest.Hash([]byte(data)), Mode: "0644"}
	}
	for _, path := range resource.Paths(name) {
		write(path, "generated\n", manifest.Scaffold, "gen/resource")
	}
	write("web/src/routes/posts/+page.svelte", "<h1 />\n", manifest.Scaffold, "gen/resource")
	write("web/src/routes/posts/schema.ts", "export {};\n", manifest.Scaffold, "gen/resource")
	write("web/src/lib/nav.gen.ts", "old\n", manifest.Managed, "web/files/web/src/lib/nav.gen.ts")
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	plan, err := Build(context.Background(), Options{Root: root, Kind: "resource", Name: "todo"}, lock)
	if err != nil {
		t.Fatal(err)
	}
	nav, ok := plan.Files.Get("web/src/lib/nav.gen.ts")
	if !ok || nav.Class != string(manifest.Managed) {
		t.Fatalf("navigation file = %#v, present = %v", nav, ok)
	}
	if got := string(nav.Data); !strings.Contains(got, "href: '/posts'") || strings.Contains(got, "href: '/todos'") {
		t.Fatalf("navigation = %s", got)
	}
}

func TestPostSteps(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	lock := manifest.New()
	write := func(path, data string, tracked bool) {
		t.Helper()
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
		if tracked {
			lock.Files[path] = manifest.FileEntry{Class: manifest.Managed, Template: "gen/resource", Hash: manifest.Hash([]byte(data)), Mode: "0644"}
		}
	}
	write("api/openapi/openapi.yaml", "openapi: 3.0.3\ninfo:\n  title: Test\n  version: 1.0.0\npaths: {}\n", false)
	write("api/openapi/paths/others.yaml", "/api/others:\n  get:\n    operationId: ListOthers\n    responses:\n      '200':\n        description: OK\n", false)
	write("api/db/queries/others.sql", "-- name: ListOthers :many\nSELECT 1;\n", false)
	write("api/db/queries/todos.sql", "original", true)
	write("api/internal/routes/routes_gen.go", "package routes\n", true)
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	// The OpenAPI and sqlc post-steps run at the same time.
	fake := (&exec.Fake{}).AnyOrder()
	fake.Expect("go", "tool", "-modfile=tools.mod", "oapi-codegen").Return("", "", 0)
	fake.Expect("go", "tool", "-modfile=tools.mod", "sqlc", "generate").Return("", "", 0)
	fake.Expect("gofmt", "-w", filepath.Join("internal", "routes", "routes_gen.go")).Return("", "", 0)
	if err := Run(context.Background(), Options{Root: root, Kind: "resource", Name: "todo"}, fake); err != nil {
		t.Fatal(err)
	}
	fake.AssertDone(t)
	for _, call := range fake.Calls() {
		if call.Dir != filepath.Join(root, "api") {
			t.Fatal(call.Dir)
		}
	}
}

// TestBuildPageFamilies checks the page paths of both families (ADR 0039
// §10.5). Destroy removes the unchanged pages of either family and keeps an
// edited page. A path that only the other family writes is a candidate only
// when the lock records gen/resource as its template, so a page of another
// generator stays and destroy does not report it.
func TestBuildPageFamilies(t *testing.T) {
	t.Parallel()
	const dir = "web/src/routes/todos/"
	tests := []struct {
		name    string
		embed   bool
		files   map[string]string // path to lock template
		edited  []string
		remove  []string
		kept    []string
		ignored []string
	}{
		{
			name:  "embed",
			embed: true,
			files: map[string]string{
				dir + "+page.svelte": "gen/resource", dir + "+page.ts": "gen/resource", dir + "form.ts": "gen/resource",
				dir + "schema.ts": "gen/resource", dir + "fields.svelte": "gen/resource", dir + "new/+page.ts": "gen/resource",
				dir + "[id]/+page.ts": "gen/resource", dir + "[id]/edit/+page.svelte": "gen/resource",
				// A server load of node mode that spinup g resource wrote before.
				dir + "[id]/+page.server.ts": "gen/resource",
				// A tracked file of another owner at a node-only path.
				dir + "+page.server.ts": "web/files/web/src/routes/todos/+page.server.ts",
			},
			edited:  []string{dir + "[id]/edit/+page.svelte"},
			remove:  []string{dir + "+page.svelte", dir + "+page.ts", dir + "[id]/+page.server.ts", dir + "[id]/+page.ts", dir + "fields.svelte", dir + "form.ts", dir + "new/+page.ts", dir + "schema.ts"},
			kept:    []string{dir + "[id]/edit/+page.svelte"},
			ignored: []string{dir + "+page.server.ts"},
		},
		{
			name: "node",
			files: map[string]string{
				dir + "+page.svelte": "gen/resource", dir + "+page.server.ts": "gen/resource", dir + "schema.ts": "gen/resource",
				// A legacy page file stays a candidate.
				dir + "form.ts": "gen/resource",
				// A page load of spinup g page at an embed-only path.
				dir + "+page.ts": "gen/page",
				// A browser load of embed mode that spinup g resource wrote.
				dir + "new/+page.ts": "gen/resource",
			},
			remove:  []string{dir + "+page.server.ts", dir + "+page.svelte", dir + "form.ts", dir + "new/+page.ts", dir + "schema.ts"},
			ignored: []string{dir + "+page.ts"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			lock := manifest.New()
			if tc.embed {
				lock.Answers["frontend_mode"] = "embed"
			}
			for path, template := range tc.files {
				p := filepath.Join(root, filepath.FromSlash(path))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("original"), 0o644); err != nil {
					t.Fatal(err)
				}
				lock.Files[path] = manifest.FileEntry{Class: manifest.Scaffold, Template: template, Hash: manifest.Hash([]byte("original")), Mode: "0644"}
			}
			for _, path := range tc.edited {
				if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte("changed"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
				t.Fatal(err)
			}
			plan, err := Build(context.Background(), Options{Root: root, Kind: "resource", Name: "todo"}, lock)
			if err != nil {
				t.Fatal(err)
			}
			var removed []string
			for _, f := range plan.Files.Files {
				if f.Delete {
					removed = append(removed, f.Path)
				}
			}
			if strings.Join(removed, "\n") != strings.Join(tc.remove, "\n") {
				t.Errorf("removed:\n%s\nwant:\n%s", strings.Join(removed, "\n"), strings.Join(tc.remove, "\n"))
			}
			if strings.Join(plan.Kept, "\n") != strings.Join(tc.kept, "\n") {
				t.Errorf("kept %v, want %v", plan.Kept, tc.kept)
			}
			for _, path := range tc.ignored {
				if _, ok := plan.Files.Get(path); ok {
					t.Errorf("destroy claims %s", path)
				}
			}
		})
	}
}
