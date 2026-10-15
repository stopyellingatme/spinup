package newapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/plugin/pack"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

func TestNewPlugins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	source, err := filepath.Abs("../../../testdata/plugins/example")
	if err != nil {
		t.Fatal(err)
	}
	options := Options{Name: "demo", Dir: t.TempDir(), APIOnly: true, Version: "dev", Plugins: []string{"path:" + source + "@1.0.0"}, Answers: map[string]any{"modules": ""}}
	if _, err := Generate(context.Background(), options, nil); err == nil || !strings.Contains(err.Error(), "--trust") {
		t.Fatalf("trust err=%v", err)
	}
	options.Trust = true
	result, err := Generate(context.Background(), options, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(result.Dir); !os.IsNotExist(err) {
		t.Fatal("Generate created target")
	}
	if file, ok := result.Plan.Get("example.txt"); !ok || file.Template != "plugin/example/files/example.txt.tmpl" {
		t.Fatalf("file=%+v", file)
	}
	if _, ok := result.Plan.Get(".spinup/plugins.allow"); !ok {
		t.Fatal("missing allowlist")
	}
	if _, tracked := result.Lock.Files[".spinup/plugins.allow"]; tracked {
		t.Fatal("allowlist is CLI metadata")
	}
	if len(result.Lock.Plugins) != 1 {
		t.Fatal("missing plugin lock")
	}
	if result.Lock.Plugins[0].Version != "1.0.0" {
		t.Fatal("incorrect rendered version")
	}
	if _, ok := result.Plan.Get(".spinup/plugins.history"); !ok {
		t.Fatal("missing plugin history")
	}
	cfg, _ := result.Plan.Get("spinup.toml")
	if strings.Contains(string(cfg.Data), "path:"+source) {
		t.Fatal("declaration contains an absolute plugin path")
	}
	if !strings.Contains(string(cfg.Data), "[[plugins]]") {
		t.Fatal("missing declaration")
	}
}

func TestNewRelativePluginSurvivesRelocation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	parent := t.TempDir()
	source := filepath.Join(parent, "plugins/example")
	if err := os.CopyFS(source, os.DirFS("../../../testdata/plugins/example")); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, source)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Generate(t.Context(), Options{Name: "demo", Dir: parent, APIOnly: true, NoGit: true, Version: "dev", Trust: true, Plugins: []string{"path:" + relative}, Answers: map[string]any{"modules": ""}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"spinup.toml", ".spinup/plugins.allow", ".spinup/plugins.history"} {
		file, ok := result.Plan.Get(path)
		if !ok || !strings.Contains(string(file.Data), "path:../plugins/example") || strings.Contains(string(file.Data), parent) {
			t.Fatalf("%s: %s", path, file.Data)
		}
	}
	if err := os.MkdirAll(result.Dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := vfs.Apply(result.Dir, result.Plan, vfs.ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := result.Lock.Save(filepath.Join(result.Dir, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(t.TempDir(), "relocated")
	if err := os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}
	if err := (&pack.Manager{Root: filepath.Join(moved, "demo"), CLIVersion: "dev"}).Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
}
