package templates_test

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestGoldenSecrets(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "secrets")
	render := func() (fs.FS, error) {
		all := testutil.RenderPack(t, []string{"base", "api", "secrets"}, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": "ci,secrets"})
		selected := fstest.MapFS{}
		for _, path := range []string{".sops.yaml", "secrets/.gitkeep"} {
			data, err := fs.ReadFile(all, path)
			if err != nil {
				return nil, err
			}
			selected[path] = &fstest.MapFile{Data: data, Mode: 0644}
		}
		return selected, nil
	}
	fsys, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "secrets", fsys)
	testutil.Deterministic(t, render)
}
