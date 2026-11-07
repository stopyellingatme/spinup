package templates_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/doctor"
	_ "github.com/stopyellingatme/spinup/internal/doctor/apikeyswired"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestGoldenApikeys(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "apikeys")
	// render selects files of one pack set. The golden tree holds them under prefix.
	render := func(packs []string, modules string, paths []string, prefix string) func() (fs.FS, error) {
		return func() (fs.FS, error) {
			all := testutil.RenderPack(t, packs, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": modules})
			selected := fstest.MapFS{}
			for _, path := range paths {
				data, err := fs.ReadFile(all, path)
				if err != nil {
					return nil, err
				}
				selected[filepath.ToSlash(filepath.Join(prefix, path))] = &fstest.MapFile{Data: data, Mode: 0o644}
			}
			return selected, nil
		}
	}
	apikeysOnly := []string{
		"api/cmd/api/main.go",
		"api/db/kit/00009_api_keys.sql",
		"api/internal/apikeys/apikeys.gen.go",
		"api/openapi/openapi.yaml",
		"web/src/lib/apikeys.gen.ts",
		"web/src/lib/components/app/user-menu.svelte",
		"web/src/lib/server/apikeys.ts",
		"web/src/routes/+layout.svelte",
		"web/src/routes/admin/apikeys/+page.server.ts",
		"web/src/routes/admin/apikeys/+page.svelte",
		"web/src/routes/admin/apikeys/schema.ts",
		"web/src/routes/settings/api-keys/+page.server.ts",
		"web/src/routes/settings/api-keys/+page.svelte",
		"web/src/routes/settings/api-keys/schema.ts",
	}
	withFlags := []string{
		"api/cmd/api/main.go",
		"web/src/lib/components/app/user-menu.svelte",
		"web/src/routes/+layout.svelte",
	}
	renderOnly := render([]string{"base", "api", "web", "auth", "apikeys"}, "auth,apikeys", apikeysOnly, "")
	renderBoth := render([]string{"base", "api", "web", "auth", "flags", "apikeys"}, "auth,flags,apikeys", withFlags, "both")
	merged := func() (fs.FS, error) {
		first, err := renderOnly()
		if err != nil {
			return nil, err
		}
		second, err := renderBoth()
		if err != nil {
			return nil, err
		}
		out := fstest.MapFS{}
		for _, part := range []fs.FS{first, second} {
			if err := fs.WalkDir(part, ".", func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				data, err := fs.ReadFile(part, path)
				out[path] = &fstest.MapFile{Data: data, Mode: 0o644}
				return err
			}); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	fsys, err := merged()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "apikeys", fsys)
	testutil.Deterministic(t, merged)

	var check *doctor.Check
	for _, c := range doctor.Checks(doctor.Local, true) {
		if c.ID == "apikeys-wired" {
			check = &c
			break
		}
	}
	if check == nil {
		t.Fatal("apikeys-wired is not registered")
	}
	for _, tree := range []struct {
		name, prefix string
		packs        []string
	}{
		{"apikeys", "", []string{"api", "auth", "apikeys"}},
		{"both", "both", []string{"api", "auth", "flags", "apikeys"}},
	} {
		root := t.TempDir()
		mainPath := filepath.ToSlash(filepath.Join(tree.prefix, "api/cmd/api/main.go"))
		data, err := fs.ReadFile(fsys, mainPath)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, "api", "cmd", "api", "main.go")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatal(err)
		}
		lock := manifest.New()
		lock.Packs = slices.Clone(tree.packs)
		if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
			t.Fatal(err)
		}
		if got := check.Run(t.Context(), &doctor.Context{Root: root}); got.Status != doctor.OK {
			t.Fatalf("%s: rendered wiring=%+v", tree.name, got)
		}
	}
}
