package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDBSubcommandsRegistered(t *testing.T) {
	t.Parallel()
	var out, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"db", "--help"}, nil, &out, &stderr); code != 0 {
		t.Fatalf("exit=%d, stderr=%s", code, stderr.String())
	}
	for _, name := range []string{"migrate", "status", "redo", "new", "to", "seed", "reset", "psql", "dump", "restore", "doctor", "sqlc"} {
		if !strings.Contains(out.String(), "\n  "+name+" ") {
			t.Errorf("db help lacks %q:\n%s", name, out.String())
		}
	}
}

func TestDBCommandsWiring(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	config := "[app]\nname='todo'\n[env.production]\nprotected=true\n"
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args     []string
		code     int
		out, err string
	}{
		{args: []string{"db", "new", "add_widgets", "--dry-run"}, out: "Would create api/db/migrations/00001_add_widgets.sql\n"},
		{args: []string{"db", "redo", "--dry-run"}, code: 1, err: "Try: spinup db redo --i-know"},
		{args: []string{"db", "redo", "--i-know", "--dry-run", "--non-interactive"}, code: 1, err: "protected environment"},
		{args: []string{"db", "to", "0", "--dry-run", "--env", "production"}, out: "go run ./cmd/api migrate to 0\n"},
		{args: []string{"db", "reset", "--env", "production", "--yes", "--dry-run"}, code: 1, err: "works only on the local development database"},
		{args: []string{"db", "dump", "a.dump", "--dry-run"}, out: "docker compose -f deploy/compose.dev.yml exec -T postgres pg_dump -U app -d app -Fc > a.dump\n"},
		{args: []string{"db", "restore", "--dry-run"}, code: 2, err: "Try: spinup db restore --help"},
		{args: []string{"db", "to"}, code: 2},
		{args: []string{"db", "sqlc"}, out: "Try: spinup g resource <name> <field:type>...\n"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			var out, stderr bytes.Buffer
			code := Execute(context.Background(), append(tc.args, "--dir", root), nil, &out, &stderr)
			if code != tc.code || !strings.HasSuffix(out.String(), tc.out) || !strings.Contains(stderr.String(), tc.err) {
				t.Fatalf("exit=%d, out=%q, stderr=%q", code, out.String(), stderr.String())
			}
		})
	}
}
