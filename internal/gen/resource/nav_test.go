package resource

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen"
	genrun "github.com/stopyellingatme/spinup/internal/gen/run"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

func TestPlanNav(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		routes []string
		want   string
	}{
		{"none", nil, "export const nav: NavItem[] = [\n];\nexport const home: string = '/';\n"},
		{"one", []string{"todos"}, "\t{ href: '/todos', label: 'Todos' },\n];\nexport const home: string = '/todos';\n"},
		{"sorted", []string{"zebras", "apples", "notes"}, "\t{ href: '/apples', label: 'Apples' },\n\t{ href: '/notes', label: 'Notes' },\n\t{ href: '/zebras', label: 'Zebras' },\n];\nexport const home: string = '/apples';\n"},
		{"hyphenated", []string{"blog-posts"}, "\t{ href: '/blog-posts', label: 'Blog Posts' },\n];\nexport const home: string = '/blog-posts';\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := webApp(t)
			for _, route := range tc.routes {
				write(t, root, map[string]string{
					"web/src/routes/" + route + "/+page.svelte": "<h1 />\n",
					"web/src/routes/" + route + "/schema.ts":    "export {};\n",
				})
			}
			plan, err := PlanNav(root, vfs.Plan{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			file, ok := plan.Get(navPath)
			if !ok {
				t.Fatal("navigation file is missing")
			}
			if file.Class != string(manifest.Managed) {
				t.Fatalf("class = %q, want managed", file.Class)
			}
			if !strings.HasSuffix(string(file.Data), tc.want) {
				t.Fatalf("navigation =\n%s\nwant suffix:\n%s", file.Data, tc.want)
			}
		})
	}
}

func TestBuildAddsManagedNav(t *testing.T) {
	t.Parallel()
	root := webApp(t)
	pages := Part{Name: "pages", Plan: func(context.Context, Spec) (vfs.Plan, []gen.PostStep, error) {
		return vfs.Plan{Files: []vfs.File{
			{Path: "web/src/routes/todos/+page.svelte", Data: []byte("<h1 />\n"), Class: string(manifest.Scaffold)},
			{Path: "web/src/routes/todos/schema.ts", Data: []byte("export {};\n"), Class: string(manifest.Scaffold)},
		}}, nil, nil
	}}
	out, err := build(context.Background(), gen.Input{Root: root, Args: []string{"todo", "title:string"}, Flags: flagSet(t)}, []Part{pages})
	if err != nil {
		t.Fatal(err)
	}
	file, ok := out.Plan.Get(navPath)
	if !ok || file.Class != string(manifest.Managed) {
		t.Fatalf("navigation file = %#v, present = %v", file, ok)
	}
	if !strings.Contains(string(file.Data), "href: '/todos'") {
		t.Fatalf("navigation = %s", file.Data)
	}
}

func TestNavIsGeneratorOwnedAfterEdit(t *testing.T) {
	t.Parallel()
	root := webApp(t)
	lock, err := manifest.Load(filepath.Join(root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	initial, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(navPath)))
	if err != nil {
		t.Fatal(err)
	}
	lock.Files[navPath] = manifest.FileEntry{Class: manifest.Managed, Template: "web/files/web/src/lib/nav.gen.ts", Hash: manifest.Hash(initial), Mode: "0644"}
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	write(t, root, map[string]string{
		"web/src/routes/todos/+page.svelte": "<h1 />\n",
		"web/src/routes/todos/form.ts":      "export {};\n",
	})
	g := gen.Generator{Name: "resource", Run: func(context.Context, gen.Input) (*gen.Output, error) {
		plan, err := PlanNav(root, vfs.Plan{}, nil)
		return &gen.Output{Plan: plan}, err
	}}
	if _, err := genrun.Run(context.Background(), g, nil, nil, genrun.Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(navPath)), []byte("user change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := genrun.Run(context.Background(), g, nil, nil, genrun.Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(navPath)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "user change") {
		t.Fatalf("navigation was not rebuilt: %s", data)
	}
	lock, err = manifest.Load(filepath.Join(root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Files[navPath].Template != "gen/resource" {
		t.Fatalf("template = %q, want gen/resource", lock.Files[navPath].Template)
	}
}
