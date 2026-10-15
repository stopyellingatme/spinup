package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildImageDryRun(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "api"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte("[app]\nname = 'todo'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api", "go.mod"), []byte("module github.com/acme/todo/api\n"), 0644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run(t, "build", "--image", "--push", "--tag", "v1", "--dry-run", "--dir", root)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "docker buildx build") || !strings.Contains(out, "--provenance=mode=max") || !strings.Contains(out, "ghcr.io/acme/todo-web:v1") {
		t.Fatalf("output=%q", out)
	}
}
