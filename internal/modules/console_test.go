package modules

import (
	"reflect"
	"slices"
	"testing"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/render"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
)

func TestConsoleCatalogAndOldLock(t *testing.T) {
	t.Parallel()
	m, ok := Get("console")
	if !ok || m.Name != "console" || m.Description != "Operator overview of one deployed app." ||
		!slices.Equal(m.Requires, []string{"deploy"}) || m.NeedsWeb {
		t.Fatalf("console metadata = %+v, found = %v", m, ok)
	}
	fsys := templates.Embedded()
	if _, err := templates.Load(fsys, "console"); err != nil {
		t.Fatal(err)
	}
	// This lock predates console. Rendering current packs must retain its answers.
	lock, err := manifest.Parse([]byte(`{"schema":2,"packs":["base","api","ci","deploy"],"answers":{"app_name":"todo","module":"example.com/todo","modules":["ci","deploy"],"frontend_mode":"node"}}`))
	if err != nil {
		t.Fatal(err)
	}
	before, err := lock.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var packs []*templates.Pack
	for _, name := range lock.Packs {
		pack, err := templates.Load(fsys, name)
		if err != nil {
			t.Fatal(err)
		}
		packs = append(packs, pack)
	}
	plan, err := render.Render(render.Input{Packs: packs, Answers: lock.Answers, Stack: stackversions.MustLoad(), TemplateVersion: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range plan.Files {
		if file.Path != "spinup.toml" {
			continue
		}
		cfg, err := project.Parse(file.Data)
		if err != nil {
			t.Fatal(err)
		}
		if Enabled(cfg, "console") || !slices.Equal(cfg.Modules, []string{"ci", "deploy"}) {
			t.Fatalf("old lock acquired console: %v", cfg.Modules)
		}
		after, err := lock.Marshal()
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("render changed the old lock: %v", err)
		}
		return
	}
	t.Fatal("spinup.toml is missing from the plan")
}
