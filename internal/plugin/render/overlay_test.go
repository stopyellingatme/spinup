package render

import (
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/plugin"
	corerender "github.com/stopyellingatme/spinup/internal/render"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

func TestOverlay(t *testing.T) {
	t.Parallel()
	core := fstest.MapFS{"api/pack.toml": {Data: []byte("name = 'api'\n")}}
	loaded := plugin.Loaded{Meta: plugin.Meta{Name: "example", Version: "1.0.0", API: 1}, FS: fstest.MapFS{"pack.toml": {Data: []byte("name = 'example'\nrequires = ['api']\n")}}}
	v, names, err := Overlay(&templates.Version{Version: "0.1.0", Packs: core}, []string{"api"}, []plugin.Loaded{loaded})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[1] != "example" {
		t.Fatalf("names = %v", names)
	}
	root, err := v.Packs.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if entries, err := root.(fs.ReadDirFile).ReadDir(-1); err != nil || len(entries) != 2 {
		t.Fatalf("root entries=%v err=%v", entries, err)
	}
	if _, err := templates.Load(v.Packs, "example"); err != nil {
		t.Fatal(err)
	}
	if entries, err := fs.ReadDir(v.Packs, "."); err != nil || len(entries) != 2 {
		t.Fatalf("entries=%v, err=%v", entries, err)
	}
	if _, _, err := Overlay(v, names, []plugin.Loaded{loaded}); err == nil {
		t.Fatal("accepted collision")
	}
	if _, _, err := Overlay(&templates.Version{Packs: core}, nil, []plugin.Loaded{loaded}); err == nil {
		t.Fatal("accepted missing dependency")
	}
}

func TestRenderIgnoresPluginSource(t *testing.T) {
	t.Parallel()
	core := fstest.MapFS{
		"api/pack.toml":   {Data: []byte("name='api'\n[[files]]\npath='spinup.toml'\nsource='config.tmpl'\nclass='scaffold'\n")},
		"api/config.tmpl": {Data: []byte("[app]\nname='demo'\n")},
	}
	p := plugin.Loaded{Meta: plugin.Meta{Name: "example", Version: "1.0.0"}, SHA256: strings.Repeat("a", 64), FS: fstest.MapFS{
		"pack.toml":          {Data: []byte("name='example'\n[[files]]\npath='plugin.txt'\nsource='files/content.tmpl'\nclass='managed'\n")},
		"files/content.tmpl": {Data: []byte("[[ .name ]]\n")},
	}}
	var first vfs.Plan
	for i, source := range []string{"path:/first", "path:/second"} {
		p.Source = source
		version, names, err := Overlay(&templates.Version{Version: "0.1.0", Packs: core}, []string{"api"}, []plugin.Loaded{p})
		if err != nil {
			t.Fatal(err)
		}
		var packs []*templates.Pack
		for _, name := range names {
			pack, err := templates.Load(version.Packs, name)
			if err != nil {
				t.Fatal(err)
			}
			packs = append(packs, pack)
		}
		plan, err := corerender.Render(corerender.Input{Packs: packs, Answers: map[string]any{"name": "demo"}, TemplateVersion: version.Version})
		if err != nil {
			t.Fatal(err)
		}
		plan = Prefix(version.Packs, plan)
		if i == 0 {
			first = plan
		} else if !reflect.DeepEqual(first, plan) {
			t.Fatalf("render depends on source: %v != %v", first, plan)
		}
	}
}
