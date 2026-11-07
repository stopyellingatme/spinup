package templates_test

import (
	"io/fs"
	"testing"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestGoldenMCP(t *testing.T) {
	t.Parallel()
	if !testutil.GoldenSelected(t, "mcp") {
		return
	}
	render := func() (fs.FS, error) {
		return testutil.RenderPack(t, []string{"base", "api", "web", "auth", "apikeys", "mcp", "deploy"}, map[string]any{
			"app_name": "todo",
			"module":   "example.com/todo",
			"modules":  "auth,apikeys,mcp",
		}), nil
	}
	fsys, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "mcp", fsys)
	testutil.Deterministic(t, render)
}
