package templates_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/doctor"
	_ "github.com/stopyellingatme/spinup/internal/doctor/flagswired"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestGoldenFlags(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "flags")
	render := func() (fs.FS, error) {
		all := testutil.RenderPack(t, []string{"base", "api", "web", "auth", "flags"}, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": "auth,flags"})
		selected := fstest.MapFS{}
		for _, path := range []string{"api/cmd/api/main.go", "api/db/kit/00008_feature_flags.sql", "api/internal/flags/flags.gen.go", "web/src/lib/flags.gen.ts"} {
			data, err := fs.ReadFile(all, path)
			if err != nil {
				return nil, err
			}
			selected[path] = &fstest.MapFile{Data: data, Mode: 0o644}
		}
		return selected, nil
	}
	fsys, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "flags", fsys)
	testutil.Deterministic(t, render)
	root := t.TempDir()
	if err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	lock := manifest.New()
	lock.Packs = []string{"api", "auth", "flags"}
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	for _, check := range doctor.Checks(doctor.Local, true) {
		if check.ID == "flags-wired" {
			if got := check.Run(t.Context(), &doctor.Context{Root: root}); got.Status != doctor.OK {
				t.Fatalf("rendered wiring=%+v", got)
			}
			return
		}
	}
	t.Fatal("flags-wired is not registered")
}
