package templates_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestGoldenCi(t *testing.T) {
	if !testutil.GoldenSelected(t, "ci") {
		return
	}
	t.Parallel()

	render := func() (fs.FS, error) {
		return testutil.RenderPack(t, []string{"ci"}, nil), nil
	}
	fsys := testutil.RenderPack(t, []string{"ci"}, nil)
	testutil.Golden(t, "ci", fsys)
	testutil.Deterministic(t, render)

	for _, version := range []string{"18", "17", "16"} {
		t.Run("postgres-"+version, func(t *testing.T) {
			t.Parallel()
			fsys := testutil.RenderPack(t, []string{"ci"}, map[string]any{"db_version": version})
			data, err := fs.ReadFile(fsys, ".github/workflows/ci.yml")
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
				t.Fatalf("CI does not use %s", image.Ref())
			}
		})
	}
}
