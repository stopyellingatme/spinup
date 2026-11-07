package devkit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsDev(t *testing.T) {
	t.Parallel()
	for v, want := range map[string]bool{"dev": true, "0.0.0-dev": true, "v0.0.0-20261107-abc": true, "0.1.0-alpha.418": false, "v1.2.3": false} {
		if got := IsDev(v); got != want {
			t.Errorf("IsDev(%q)=%v", v, got)
		}
	}
}

func TestFind(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sub := filepath.Join(root, "bin", "x")
	for path, data := range map[string]string{"go.mod": "module github.com/stopyellingatme/spinup\n", "kit/go.mod": "module k\n", "bin/x/f": ""} {
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := find(sub); got != filepath.Join(root, "kit") {
		t.Fatalf("find=%q", got)
	}
	if got := find(t.TempDir()); got != "" {
		t.Fatalf("find=%q", got)
	}
}

func TestDirAndHint(t *testing.T) {
	t.Parallel()
	if dir := Dir(); dir == "" || !strings.HasSuffix(dir, "kit") {
		t.Fatalf("Dir=%q", dir)
	}
	if h := Hint(); !strings.HasPrefix(h, EnvReplace+"=") {
		t.Fatal(h)
	}
}
