package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestDBLint(t *testing.T) {
	t.Parallel()
	root := lintProject(t)
	writeLintMigration(t, root, "00001_drop.sql", "DROP TABLE todos;")
	code, out, errOut := runInDir(t, root, "db", "lint")
	if code != ui.ExitError || !strings.Contains(out, "00001_drop.sql:2: drop-table") || !strings.Contains(out, "Try:") || !strings.Contains(errOut, "Try:") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
}

func TestDBLintJSON(t *testing.T) {
	t.Parallel()
	root := lintProject(t)
	writeLintMigration(t, root, "00001_drop.sql", "DROP TABLE todos;")
	code, out, _ := runInDir(t, root, "--json", "db", "lint")
	var findings []map[string]any
	if code != ui.ExitError || json.Unmarshal([]byte(out), &findings) != nil || len(findings) != 1 || findings[0]["rule"] != "drop-table" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestDBLintNamedFile(t *testing.T) {
	t.Parallel()
	root := lintProject(t)
	path := writeLintMigration(t, root, "00001_safe.sql", "CREATE TABLE todos (id int);")
	code, out, errOut := runInDir(t, root, "db", "lint", path)
	if code != 0 || out != "" || errOut != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
}

func TestDBLintNamedFileFlagsIndexOnEarlierTable(t *testing.T) {
	t.Parallel()
	root := lintProject(t)
	writeLintMigration(t, root, "00001_create.sql", "CREATE TABLE todos (id int);")
	path := writeLintMigration(t, root, "00002_index.sql", "CREATE INDEX todos_id_idx ON todos (id);")
	code, out, errOut := runInDir(t, root, "db", "lint", path)
	if code != ui.ExitError || !strings.Contains(out, "index-not-concurrent") || !strings.Contains(errOut, "Try:") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
	}
}

func lintProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte("[app]\nname='todo'\nmodule='example.com/todo'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "api", "db", "migrations"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeLintMigration(t *testing.T, root, name, body string) string {
	t.Helper()
	path := filepath.Join(root, "api", "db", "migrations", name)
	if err := os.WriteFile(path, []byte("-- +goose Up\n"+body+"\n-- +goose Down\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runInDir(t *testing.T, root string, args ...string) (int, string, string) {
	t.Helper()
	args = append(args, "--dir", root)
	var out, errOut strings.Builder
	code := Execute(context.Background(), args, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}
