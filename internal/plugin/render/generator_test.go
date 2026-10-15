package render

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/gen"
	genrun "github.com/stopyellingatme/spinup/internal/gen/run"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/plugin"
)

func TestDeclarativeGenerator(t *testing.T) {
	t.Parallel()
	p := plugin.Loaded{Meta: plugin.Meta{Name: "example", Generators: []plugin.GeneratorSpec{{Name: "greeting", Args: []string{"Name"}, Files: []plugin.GenFile{{Path: "greetings/{{.Name}}.txt", Source: "files/{{.Name}}.tmpl", Class: "scaffold"}}}}}, FS: fstest.MapFS{"files/Ada.tmpl": {Data: []byte("Hello, {{.Name}}!\r\n")}}}
	all, err := Generators([]plugin.Loaded{p}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Generators([]plugin.Loaded{p}, []gen.Generator{{Name: "greeting"}}); err == nil {
		t.Fatal("accepted collision")
	}
	root := t.TempDir()
	lock := manifest.New()
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"Ada", "extra"}, {"../escape"}} {
		if _, err := all[0].Run(context.Background(), gen.Input{Root: root, Lock: lock, Args: args}); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("args=%v err=%v", args, err)
		}
	}
	for _, preview := range []genrun.Options{{DryRun: true}, {Diff: true}} {
		preview.Root = root
		if _, err := RunGenerator(context.Background(), all[0], []string{"Ada"}, nil, preview); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, "greetings/Ada.txt")); !os.IsNotExist(err) {
			t.Fatal("preview wrote output")
		}
	}
	if _, err := RunGenerator(context.Background(), all[0], []string{"Ada"}, nil, genrun.Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	lock, err = manifest.Load(filepath.Join(root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if got := lock.Files["greetings/Ada.txt"]; got.Template != "plugin/example/@gen/greeting" || got.Hash != manifest.Hash([]byte("Hello, Ada!\n")) {
		t.Fatalf("entry=%+v", got)
	}
	if err := os.WriteFile(filepath.Join(root, "greetings/Ada.txt"), []byte("user edit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := RunGenerator(context.Background(), all[0], []string{"Ada"}, nil, genrun.Options{Root: root}); err == nil {
		t.Fatal("overwrote user edit")
	}
	if _, err := RunGenerator(context.Background(), all[0], []string{"Ada"}, nil, genrun.Options{Root: root, Force: true}); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratorRejectsRenderedProtectedPaths(t *testing.T) {
	t.Parallel()
	for _, name := range []string{".GiT/config", "nested/.git/hooks/file", ".GitHub/workflows/ci.yml", "spinup.toml", "SPINUP.lock", ".Spinup/plugins.allow"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := plugin.Loaded{Meta: plugin.Meta{Name: "example"}, FS: fstest.MapFS{"files/file.tmpl": {Data: []byte("content")}}}
			spec := plugin.GeneratorSpec{Name: "file", Args: []string{"Path"}, Files: []plugin.GenFile{{Path: "{{.Path}}", Source: "files/file.tmpl", Class: "managed"}}}
			if _, err := generate(t.Context(), p, spec, gen.Input{Lock: manifest.New(), Args: []string{name}}); err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("protected path %q: %v", name, err)
			}
		})
	}
}
