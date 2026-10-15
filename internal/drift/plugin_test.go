package drift

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/plugin/pack"
	pluginrender "github.com/stopyellingatme/spinup/internal/plugin/render"
	"github.com/stopyellingatme/spinup/internal/ui"
	"github.com/stopyellingatme/spinup/internal/upgrade"
)

func TestLockedPluginDriftAfterUpdateAndRemove(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	o, lock := fixture(t)
	if err := os.WriteFile(filepath.Join(o.Dir, "spinup.toml"), []byte("[app]\nname='demo'\nmodule='example.com/demo'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.CopyFS(source, os.DirFS("../../testdata/plugins/example")); err != nil {
		t.Fatal(err)
	}
	m := &pack.Manager{Root: o.Dir, Trust: true, CLIVersion: "dev"}
	if err := m.Add(t.Context(), "path:"+source, ""); err != nil {
		t.Fatal(err)
	}
	// Install the plugin before approving different bytes.
	version := o.Source.(testSource).version
	loaded, err := plugin.Resolve(t.Context(), plugin.Options{Root: o.Dir})
	if err != nil {
		t.Fatal(err)
	}
	v, names, err := pluginrender.Overlay(version, lock.Packs, loaded)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := upgrade.Render(v, names, lock.Answers)
	if err != nil {
		t.Fatal(err)
	}
	lock.Packs = names
	lock.Plugins = pluginrender.Plugins(v.Packs)
	for _, file := range plan.Files {
		lock.Files[file.Path] = manifest.FileEntry{Class: manifest.Class(file.Class), Template: file.Template, Hash: manifest.Hash(file.Data), Mode: "0644"}
	}
	if err := lock.Save(filepath.Join(o.Dir, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "files/example.txt.tmpl"), []byte("new approval\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := m.Update(t.Context(), "example", ""); err != nil {
		t.Fatal(err)
	}
	for _, removed := range []bool{false, true} {
		if removed {
			if err := m.Remove(t.Context(), "example"); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(o.Dir, "example.txt"), []byte("edited\n"), 0644); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		if err := Diff(t.Context(), o, []string{"example.txt"}, ui.New(nil, &output, &output, ui.Options{})); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "This file came from") {
			t.Fatal(output.String())
		}
		if err := Restore(t.Context(), o, "example.txt", true); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(o.Dir, "example.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "This file came from the example plugin.\n" {
			t.Fatalf("restored NEW instead of locked content: %q", got)
		}
		ejectOptions := o
		ejectOptions.DryRun = true
		if err := Eject(t.Context(), ejectOptions, "example.txt"); err != nil {
			t.Fatal(err)
		}
		saved, err := manifest.Load(filepath.Join(o.Dir, "spinup.lock"))
		if err != nil {
			t.Fatal(err)
		}
		if saved.Files["example.txt"].Template != "plugin/example/files/example.txt.tmpl" {
			t.Fatal(saved.Files["example.txt"])
		}
	}
}
