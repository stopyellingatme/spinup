package upgrade

import (
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
)

type planSpec struct {
	path, data, class, from string
	mode                    fs.FileMode
}

func planVersion(version string, specs ...planSpec) *templates.Version {
	files := fstest.MapFS{}
	var metadata strings.Builder
	metadata.WriteString("name = 'base'\n")
	for i, spec := range specs {
		if spec.class == "" {
			spec.class = "scaffold"
		}
		if spec.mode == 0 {
			spec.mode = 0o644
		}
		source := fmt.Sprintf("file%d.tmpl", i)
		fmt.Fprintf(&metadata, "[[files]]\npath = %q\nsource = %q\nclass = %q\nmode = '%04o'\n", spec.path, source, spec.class, spec.mode)
		if spec.from != "" {
			fmt.Fprintf(&metadata, "renamed_from = %q\n", spec.from)
		}
		files["base/"+source] = &fstest.MapFile{Data: []byte(spec.data)}
	}
	files["base/pack.toml"] = &fstest.MapFile{Data: []byte(metadata.String())}
	return &templates.Version{Version: version, Packs: files, Stack: &stackversions.Stack{}}
}

func TestRenderVersion(t *testing.T) {
	t.Parallel()
	v := planVersion("0.2.0", planSpec{path: "[[ .name ]].txt", data: "[[ .TemplateVersion ]]\r\n", from: "old/[[ .name ]].txt"})
	p, err := Render(v, []string{"base"}, map[string]any{"name": "app"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 1 || p.Files[0].Path != "app.txt" || string(p.Files[0].Data) != "0.2.0\n" {
		t.Fatalf("plan: %#v", p)
	}
	renames, err := RenamedFrom(v, []string{"base"}, map[string]any{"name": "app"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(renames, map[string]string{"app.txt": "old/app.txt"}) {
		t.Fatalf("renames: %v", renames)
	}
	for _, packs := range [][]string{{"missing"}, {"base"}} {
		_, err := Render(v, packs, nil)
		if err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("error: %v", err)
		}
		if packs[0] == "missing" && (!strings.Contains(err.Error(), "missing") || !strings.Contains(err.Error(), "0.2.0")) {
			t.Fatal(err)
		}
	}
}

func TestRenderPackOrderDefaultsAndRenames(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		"first/pack.toml":  {Data: []byte("name='first'\n[[questions]]\nkey='name'\ntype='string'\ndefault='demo'\n[[files]]\npath='[[ .name ]].txt'\nsource='file.tmpl'\nclass='managed'\nrenamed_from='old/[[ .name ]].txt'\n[[files]]\npath='disabled.txt'\nsource='file.tmpl'\nclass='managed'\nrenamed_from='never.txt'\nwhen=\"name == 'other'\"\n")},
		"first/file.tmpl":  {Data: []byte("[[ .name ]] [[ .Stack.Go.Min ]] [[ .TemplateVersion ]]")},
		"second/pack.toml": {Data: []byte("name='second'\n[[files]]\npath='second.txt'\nsource='file'\nclass='seed'\n")},
		"second/file":      {Data: []byte("seed")},
	}
	stack := &stackversions.Stack{}
	stack.Go.Min = "1.26.0"
	v := &templates.Version{Version: "0.2.0", Packs: files, Stack: stack}
	p, err := Render(v, []string{"second", "first"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 2 || p.Files[0].Path != "second.txt" || p.Files[1].Path != "demo.txt" || string(p.Files[1].Data) != "demo 1.26.0 0.2.0\n" {
		t.Fatalf("render: %v", p)
	}
	renames, err := RenamedFrom(v, []string{"second", "first"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(renames, map[string]string{"demo.txt": "old/demo.txt"}) {
		t.Fatal(renames)
	}
}

func TestRenderRenamePathErrors(t *testing.T) {
	t.Parallel()
	for _, from := range []string{"[[ .missing ]]", "[[ .name ]]", "[[", "./old.txt"} {
		t.Run(from, func(t *testing.T) {
			t.Parallel()
			v := planVersion("0.2.0", planSpec{path: "new.txt", data: "new", from: from})
			_, err := RenamedFrom(v, []string{"base"}, map[string]any{"name": "../escape"})
			if err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("error: %v", err)
			}
		})
	}
}
