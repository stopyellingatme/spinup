package templates_test

import (
	"io/fs"
	"reflect"
	"testing"

	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestGoldenBase(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "base")
	answers := map[string]any{"app_name": "todo", "module": "example.com/todo"}
	render := func() (fs.FS, error) {
		return testutil.RenderPack(t, []string{"base"}, answers), nil
	}
	fsys, _ := render()
	testutil.Golden(t, "base", fsys)
	testutil.Deterministic(t, render)
	data, err := fs.ReadFile(fsys, "spinup.toml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := project.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.App.Name != "todo" || cfg.App.Module != "example.com/todo" ||
		cfg.BehaviorVersion != 1 || cfg.Monitoring.Profile != "lite" ||
		!reflect.DeepEqual(cfg.Modules, []string{"ci", "deploy"}) {
		t.Fatalf("unexpected project configuration: %+v", cfg)
	}
}
