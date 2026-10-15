package migration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen"
)

func TestMigration(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "api", "db", "migrations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"00001_init.sql", "00007_add_tags.sql"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("-- +goose Up\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"next number", []string{"add_index"}, "api/db/migrations/00008_add_index.sql"},
		{"no name", nil, "Try: spinup g migration"},
		{"not snake case", []string{"AddIndex"}, "Try: use lower-case words"},
		{"name taken", []string{"add_tags"}, "Try: choose another name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := run(context.Background(), gen.Input{Root: root, Args: tc.args})
			if strings.HasPrefix(tc.want, "Try:") {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error %v, want %q", err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			f, ok := out.Plan.Get(tc.want)
			if !ok || f.Class != "seed" || !strings.HasPrefix(string(f.Data), "-- +goose Up\n") || len(out.PostSteps) != 0 {
				t.Fatalf("plan %v, steps %v", out.Plan.Paths(), out.PostSteps)
			}
		})
	}
}
