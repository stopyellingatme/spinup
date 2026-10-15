package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/manifest"
)

func TestMaintenanceCommands(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	run := func(code int, args ...string) string {
		t.Helper()
		var out, errOut bytes.Buffer
		if got := Execute(context.Background(), args, strings.NewReader(""), &out, &errOut); got != code {
			t.Fatalf("%v: exit %d, want %d\n%s\n%s", args, got, code, &out, &errOut)
		}
		return out.String()
	}
	run(0, "new", "app", "--dir", root, "--api-only", "--no-git", "--module", "example.com/app", "--non-interactive")
	app := filepath.Join(root, "app")
	runIn := func(code int, args ...string) string {
		t.Helper()
		return run(code, append([]string{"--dir", app}, args...)...)
	}
	runIn(0, "check")
	if got := runIn(0, "diff"); got != "" {
		t.Fatalf("clean diff: %s", got)
	}
	managed := "deploy/compose.dev.yml"
	full := filepath.Join(app, filepath.FromSlash(managed))
	if err := os.WriteFile(full, []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := runIn(4, "check"); !strings.Contains(got, "spinup eject "+managed) {
		t.Fatal(got)
	}
	if got := runIn(0, "diff", managed); !strings.Contains(got, "--- a/"+managed) || !strings.Contains(got, "+changed") {
		t.Fatal(got)
	}
	if got := runIn(0, "diff", "--json"); !json.Valid([]byte(got)) {
		t.Fatal(got)
	}
	runIn(1, "restore", managed)
	runIn(0, "restore", managed, "--force", "--dry-run")
	runIn(4, "check")
	runIn(0, "eject", managed)
	runIn(0, "check")
	runIn(1, "eject", managed)
	lock, err := manifest.Load(filepath.Join(app, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Files[managed].Class != manifest.Scaffold {
		t.Fatal("eject did not change class")
	}
	if err := os.Remove(full); err != nil {
		t.Fatal(err)
	}
	if got := runIn(0, "diff", managed); !strings.Contains(got, "missing") {
		t.Fatal(got)
	}
	lock.Tombstone(managed)
	if err := lock.Save(filepath.Join(app, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	runIn(0, "restore", managed)
	runIn(0, "check")
	lock, err = manifest.Load(filepath.Join(app, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Tombstones) != 0 || lock.Files[managed].Hash == "" {
		t.Fatal("restore did not update lock")
	}
	if err := os.WriteFile(filepath.Join(app, "api/cmd/api/main.go"), []byte("=======\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := runIn(4, "check", "--json"); !json.Valid([]byte(got)) || !strings.Contains(got, "conflict") {
		t.Fatal(got)
	}
	if err := os.WriteFile(filepath.Join(app, "spinup.lock"), []byte("invalid"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := runIn(4, "check", "--json"); !json.Valid([]byte(got)) || !strings.Contains(got, "invalid-lock") {
		t.Fatal(got)
	}
}
