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
	for _, dir := range []string{"api", "deploy"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// The web image exists only with deploy/Dockerfile.web.
	if err := os.WriteFile(filepath.Join(root, "deploy", "Dockerfile.web"), []byte("FROM scratch\n"), 0644); err != nil {
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

func TestBuildImageTargetServiceDryRun(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, dir := range []string{"api", "deploy", "services/ocr"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte("[app]\nname = 'todo'\n\n[[services]]\nname = 'ocr'\nport = 8080\ndev_port = 8101\nhealth = '/healthz'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api", "go.mod"), []byte("module github.com/acme/todo/api\n"), 0644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run(t, "build", "--image", "--target", "ocr", "--dry-run", "--dir", root)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	for _, want := range []string{"-f services/ocr/Dockerfile", "VERSION=dev", "--load services/ocr"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q: %q", want, out)
		}
	}
}
