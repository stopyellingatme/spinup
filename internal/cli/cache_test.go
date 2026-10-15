package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestCacheOfflineWarm(t *testing.T) {
	t.Parallel()
	code, _, stderr := run(t, "cache", "warm", "1.0.0", "--offline")
	if code != ui.ExitUsage || !strings.Contains(stderr, "Try:") {
		t.Fatalf("code=%d, stderr=%s", code, stderr)
	}
}

func TestCacheList(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := run(t, "cache", "list", "--offline")
	if code != 0 || !strings.Contains(stdout, "0.0.0-dev\tcurrent") {
		t.Fatalf("code=%d, out=%q, err=%q", code, stdout, stderr)
	}
	code, stdout, stderr = run(t, "cache", "list", "--offline", "--json")
	var entries []struct{ Version, Source string }
	if code != 0 || json.Unmarshal([]byte(stdout), &entries) != nil || len(entries) == 0 {
		t.Fatalf("code=%d, out=%q, err=%q", code, stdout, stderr)
	}
}

func TestCacheEnvironmentOffline(t *testing.T) {
	t.Setenv("SPINUP_OFFLINE", "1")
	code, _, stderr := run(t, "cache", "warm", "1.0.0", "--offline=false")
	if code != ui.ExitUsage || !strings.Contains(stderr, "Try:") {
		t.Fatalf("code=%d, stderr=%s", code, stderr)
	}
	app := &App{Flags: &GlobalFlags{}, UI: ui.New(nil, nil, nil, ui.Options{})}
	entered := false
	root := newRoot(app, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, &entered)
	if flag := root.PersistentFlags().Lookup("offline"); flag == nil || flag.DefValue != "true" {
		t.Fatalf("offline flag=%v", flag)
	}
}

func TestCacheClean(t *testing.T) {
	// HOME selects the cache root on macOS. XDG_CACHE_HOME selects it on Linux.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", home)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cache, "spinup", "templates")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "marker")
	if err := os.WriteFile(file, []byte("keep until clean"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, dry := range []bool{true, false} {
		args := []string{"cache", "clean"}
		if dry {
			args = append(args, "--dry-run")
		}
		code, out, stderr := run(t, args...)
		if code != 0 || !strings.Contains(out, dir) {
			t.Fatalf("code=%d, out=%q, err=%q", code, out, stderr)
		}
		_, err := os.Stat(file)
		if dry && err != nil {
			t.Fatal("dry run removed cache")
		}
		if !dry && !os.IsNotExist(err) {
			t.Fatalf("cache still exists: %v", err)
		}
	}
}

func TestCacheWarmProject(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	code, _, stderr := run(t, "new", "demo", "--api-only", "--module", "example.com/demo", "--no-git", "--non-interactive", "--dir", dir)
	if code != 0 {
		t.Fatalf("new code=%d, err=%s", code, stderr)
	}
	child := filepath.Join(dir, "demo", "api")
	code, out, stderr := run(t, "cache", "warm", "--dir", child)
	if code != 0 || !strings.Contains(out, "0.0.0-dev") {
		t.Fatalf("code=%d, out=%q, err=%q", code, out, stderr)
	}
	code, _, stderr = run(t, "cache", "warm", "--dir", t.TempDir())
	if code != ui.ExitError || !strings.Contains(stderr, "Try:") {
		t.Fatalf("code=%d, err=%q", code, stderr)
	}
}

func TestGlobalOfflineFlag(t *testing.T) {
	t.Parallel()
	app := &App{Flags: &GlobalFlags{}, UI: ui.New(nil, nil, nil, ui.Options{})}
	entered := false
	root := newRoot(app, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, &entered)
	root.SetArgs([]string{"version", "--offline"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !app.Flags.Offline {
		t.Fatal("offline flag did not propagate")
	}
}
