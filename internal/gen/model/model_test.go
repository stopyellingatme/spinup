package model

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen"
)

func app(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "api", "db", "migrations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "00001_init.sql"), []byte("-- +goose Up\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestModel(t *testing.T) {
	t.Parallel()
	root := app(t)
	out, err := run(context.Background(), gen.Input{Root: root, Args: []string{"tag", "name:string"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"api/db/migrations/00002_create_tags.sql", "api/db/queries/tags.sql", "api/openapi/schemas/tag.yaml"}
	if got := out.Plan.Paths(); !reflect.DeepEqual(got, want) {
		t.Fatalf("paths %v, want %v", got, want)
	}
	if want := []gen.PostStep{gen.PostOpenAPI, gen.PostSQLC}; !reflect.DeepEqual(out.PostSteps, want) {
		t.Fatalf("post-steps %v, want %v", out.PostSteps, want)
	}
	f, _ := out.Plan.Get("api/db/migrations/00002_create_tags.sql")
	if f.Class != "seed" || !strings.Contains(string(f.Data), "CREATE TABLE tags (") {
		t.Fatalf("migration %s:\n%s", f.Class, f.Data)
	}
}

func TestModelRefusals(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{}, {"tag"}, {"tag", "name"}} {
		_, err := run(context.Background(), gen.Input{Root: app(t), Args: args})
		if err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("%v: error %v, want one with a Try: line", args, err)
		}
	}
}
