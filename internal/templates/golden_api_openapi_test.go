package templates_test

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestGoldenApiOpenapi(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "api-openapi")
	render := func() (fs.FS, error) {
		all := testutil.RenderPack(t, []string{"base", "api"}, map[string]any{"app_name": "todo", "module": "example.com/todo"})
		selected := fstest.MapFS{}
		err := fs.WalkDir(all, ".", func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			selectedPath := strings.HasPrefix(path, "api/openapi/") || path == "api/oapi-codegen.yaml" || strings.HasPrefix(path, "api/internal/gen/")
			if !selectedPath {
				return nil
			}
			data, err := fs.ReadFile(all, path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			selected[path] = &fstest.MapFile{Data: data, Mode: info.Mode()}
			return nil
		})
		return selected, err
	}
	fsys, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "api-openapi", fsys)
	testutil.Deterministic(t, render)
}
