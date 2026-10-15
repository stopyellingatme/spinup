package newapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if !strings.Contains(string(cfg.Data), "[[plugins]]") {
		t.Fatal("missing declaration")
	}
}
