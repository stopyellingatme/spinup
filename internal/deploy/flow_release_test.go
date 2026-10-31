package deploy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScaffoldEnvFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, "deploy", "environments")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	staging := []byte("smoke_urls = [\"/healthz\"]\n")
	if err := os.WriteFile(filepath.Join(dir, "staging.toml"), staging, 0644); err != nil {
		t.Fatal(err)
	}
	created, err := ScaffoldEnvFile(root, "preview")
	if err != nil || !created {
		t.Fatalf("ScaffoldEnvFile() = %v, %v", created, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "preview.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(staging) {
		t.Errorf("preview = %q, want %q", data, staging)
	}
	created, err = ScaffoldEnvFile(root, "preview")
	if err != nil || created {
		t.Errorf("second ScaffoldEnvFile() = %v, %v", created, err)
	}
	created, err = ScaffoldEnvFile(root, "staging")
	if err != nil || created {
		t.Errorf("staging ScaffoldEnvFile() = %v, %v", created, err)
	}
}
