package backup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stopyellingatme/spinup/internal/deploy"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/kit/db"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

type migrationQueryRunner struct {
	remote.Runner
	pool *pgxpool.Pool
}

// Run executes the SQL argument against the isolated test database.
func (r migrationQueryRunner) Run(ctx context.Context, c remote.Cmd) (runexec.Result, error) {
	rows, err := r.pool.Query(ctx, c.Argv[len(c.Argv)-1])
	if err != nil {
		return runexec.Result{}, err
	}
	defer rows.Close()
	var out strings.Builder
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return runexec.Result{}, err
		}
		fmt.Fprintln(&out, values[0])
	}
	return runexec.Result{Stdout: []byte(out.String())}, rows.Err()
}

func TestBackupPendingMatchesMigratorOutOfOrder(t *testing.T) {
	t.Parallel()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL is not set")
	}
	pool := testkit.DB(t)
	url := testkit.URL(t, pool)
	fsys := fstest.MapFS{
		"00001_init.sql":    {Data: []byte("-- +goose Up\nCREATE TABLE todos (id int);\n-- +goose Down\nDROP TABLE todos;\n")},
		"00006_applied.sql": {Data: []byte("-- +goose Up\n-- spinup:safe\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")},
	}
	initial, err := db.NewMigrator(url, fsys, db.WithOutput(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = initial.Close() })
	if err := initial.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	fsys["00004_drop.sql"] = &fstest.MapFile{Data: []byte("-- +goose Up\nDROP TABLE todos;\n-- +goose Down\nCREATE TABLE todos (id int);\n")}
	m, err := db.NewMigrator(url, fsys, db.WithOutput(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	dir := t.TempDir()
	for name, file := range fsys {
		if err := os.WriteFile(filepath.Join(dir, name), file.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := migrationQueryRunner{pool: pool}
	for _, phase := range []string{"before apply", "after apply"} {
		t.Run(phase, func(t *testing.T) {
			if phase == "after apply" {
				if err := m.Up(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			pending, err := m.Pending(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			applied, err := deploy.AppliedMigrations(t.Context(), runner, "todo")
			if err != nil {
				t.Fatal(err)
			}
			unsafe, err := UnsafePending(dir, applied)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"00004_drop.sql"}
			if phase == "after apply" {
				want = nil
			}
			if !slices.Equal(pending, want) || !slices.Equal(unsafe, want) {
				t.Fatalf("migrator pending = %v, backup unsafe = %v, want %v", pending, unsafe, want)
			}
		})
	}
}
