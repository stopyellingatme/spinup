package templates_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor/composelint"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestGoldenApi(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "api")
	answers := map[string]any{"app_name": "todo", "module": "example.com/todo"}
	render := func() (fs.FS, error) {
		return testutil.RenderPack(t, []string{"base", "api"}, answers), nil
	}
	fsys, _ := render()
	testutil.Golden(t, "api", fsys)
	testutil.Deterministic(t, render)

	for _, version := range []string{"18", "17", "16"} {
		t.Run("postgres-"+version, func(t *testing.T) {
			t.Parallel()
			fsys := testutil.RenderPack(t, []string{"base", "api"}, map[string]any{
				"app_name": "todo", "module": "example.com/todo", "db_version": version,
			})
			data, err := fs.ReadFile(fsys, "deploy/compose.dev.yml")
			if err != nil {
				t.Fatal(err)
			}
			key := "postgres"
			if version != "18" {
				key += "-" + version
			}
			image, err := stackversions.MustLoad().Image(key)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "image: "+image.Ref()) {
				t.Fatalf("Compose does not use %s", image.Ref())
			}
			findings, err := composelint.Lint("deploy/compose.dev.yml", data)
			if err != nil {
				t.Fatal(err)
			}
			if len(findings) != 0 {
				t.Fatalf("Compose security findings: %+v", findings)
			}
		})
	}
}
