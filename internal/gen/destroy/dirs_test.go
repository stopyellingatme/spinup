package destroy

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEmptiedDirs(t *testing.T) {
	t.Parallel()
	pages := []string{
		"web/src/routes/todos/+page.svelte",
		"web/src/routes/todos/[id]/+page.svelte",
		"web/src/routes/todos/[id]/edit/+page.svelte",
		"web/src/routes/todos/new/+page.svelte",
		"api/openapi/paths/todos.yaml",
	}
	tests := []struct {
		name  string
		extra []string
		want  []string
	}{
		{
			name:  "the resource folder empties",
			extra: []string{"web/src/routes/+page.svelte", "api/openapi/paths/.gitkeep"},
			want:  []string{"web/src/routes/todos/[id]/edit", "web/src/routes/todos/[id]", "web/src/routes/todos/new", "web/src/routes/todos"},
		},
		{
			name:  "a user file keeps its folder",
			extra: []string{"web/src/routes/+page.svelte", "api/openapi/paths/.gitkeep", "web/src/routes/todos/notes.md"},
			want:  []string{"web/src/routes/todos/[id]/edit", "web/src/routes/todos/[id]", "web/src/routes/todos/new"},
		},
		{
			name:  "a user folder keeps its parent",
			extra: []string{"web/src/routes/+page.svelte", "api/openapi/paths/.gitkeep", "web/src/routes/todos/[id]/extra/x.ts"},
			want:  []string{"web/src/routes/todos/[id]/edit", "web/src/routes/todos/new"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			for _, p := range append(append([]string{}, pages...), tc.extra...) {
				filename := filepath.Join(root, filepath.FromSlash(p))
				if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filename, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := emptiedDirs(root, pages)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("emptiedDirs = %q, want %q", got, tc.want)
			}
			for _, p := range pages {
				if err := os.Remove(filepath.Join(root, filepath.FromSlash(p))); err != nil {
					t.Fatal(err)
				}
			}
			if err := removeDirs(root, got); err != nil {
				t.Fatal(err)
			}
			for _, dir := range got {
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir))); !os.IsNotExist(err) {
					t.Fatalf("%s still exists: %v", dir, err)
				}
			}
			for _, p := range tc.extra {
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); err != nil {
					t.Fatalf("user file %s: %v", p, err)
				}
			}
		})
	}
}

func TestEmptiedDirsSkipsLinks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "web/src/routes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "web/src/routes/todos")); err != nil {
		t.Fatal(err)
	}
	got, err := emptiedDirs(root, []string{"web/src/routes/todos/+page.svelte"})
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range got {
		if dir == "web/src/routes/todos" {
			t.Fatalf("listed the linked directory: %q", got)
		}
	}
}
