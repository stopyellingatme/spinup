package templates_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestGoldenTofu(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "tofu")
	render := func() (fs.FS, error) {
		all := testutil.RenderPack(t, []string{"base", "api", "deploy", "tofu"}, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": "deploy,tofu"})
		return fs.Sub(all, "deploy/infra/tofu")
	}
	got, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "tofu", got)
	testutil.Deterministic(t, render)
}

func TestTofuPackOptIn(t *testing.T) {
	t.Parallel()
	pack, err := templates.Load(templates.Embedded(), "tofu")
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Requires) != 1 || pack.Requires[0] != "deploy" {
		t.Fatalf("requires = %v", pack.Requires)
	}
	for _, f := range pack.Files {
		want := "managed"
		if strings.HasSuffix(f.Path, "terraform.tfvars.example") {
			want = "seed"
		}
		if f.Class != want {
			t.Errorf("%s class = %s", f.Path, f.Class)
		}
	}
	all := testutil.RenderPack(t, []string{"base", "api", "deploy"}, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": "deploy"})
	err = fs.WalkDir(all, ".", func(path string, _ fs.DirEntry, err error) error {
		if strings.HasPrefix(path, "deploy/infra/tofu") {
			t.Errorf("tofu rendered without module: %s", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
