package db_test

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stopyellingatme/spinup/kit/db"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

func migrationsFS() fstest.MapFS {
	return fstest.MapFS{
		"00001_users.sql": {Data: []byte(`-- +goose Up
CREATE TABLE users (id bigint PRIMARY KEY, email text NOT NULL);

-- +goose Down
DROP TABLE users;
`)},
		"00002_notes.sql": {Data: []byte(`-- +goose Up
CREATE TABLE notes (id bigint PRIMARY KEY, body text NOT NULL);

-- +goose Down
DROP TABLE notes;
`)},
	}
}

// runCmd runs the migrate command and returns its output.
func runCmd(t *testing.T, url string, fsys fstest.MapFS, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	cmd := db.MigrateCommand(fsys, db.WithDatabaseURL(url), db.WithOutput(&out))
	if err := cmd(context.Background(), args); err != nil {
		t.Fatalf("migrate %v: %v\noutput:\n%s", args, err, out.String())
	}
	return out.String()
}

func TestMigrateCommandLifecycle(t *testing.T) {
	t.Parallel()
	url := testkit.URL(t, testkit.DB(t))
	fsys := migrationsFS()
	steps := []struct {
		args []string
		want string
	}{
		{[]string{"status"}, "pending  00001_users.sql\npending  00002_notes.sql\n"},
		{[]string{"up"}, "migrate: 2 pending\napplied  00001_users.sql\napplied  00002_notes.sql\n"},
		{[]string{"up"}, "migrate: no pending migrations\n"},
		{[]string{"status"}, "applied  00001_users.sql\napplied  00002_notes.sql\n"},
		{[]string{"down"}, "rolled back  00002_notes.sql\n"},
		{[]string{"status"}, "applied  00001_users.sql\npending  00002_notes.sql\n"},
		{[]string{"down"}, "rolled back  00001_users.sql\n"},
		{[]string{"down"}, "migrate: nothing to roll back\n"},
		{[]string{"up"}, "migrate: 2 pending\napplied  00001_users.sql\napplied  00002_notes.sql\n"},
	}
	for i, s := range steps {
		if got := runCmd(t, url, fsys, s.args...); got != s.want {
			t.Fatalf("step %d: migrate %v output:\n%s\nwant:\n%s", i, s.args, got, s.want)
		}
	}
}

func TestMigrateCommandNoMigrations(t *testing.T) {
	t.Parallel()
	url := testkit.URL(t, testkit.DB(t))
	empty := fstest.MapFS{}
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"up"}, "migrate: no pending migrations\n"},
		{[]string{"down"}, "migrate: nothing to roll back\n"},
		{[]string{"status"}, ""},
	}
	for _, tt := range tests {
		if got := runCmd(t, url, empty, tt.args...); got != tt.want {
			t.Errorf("migrate %v output %q, want %q", tt.args, got, tt.want)
		}
	}
}

func TestMigrateCommandErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		opts []db.MigrateOption
		want []string
	}{
		{"no subcommand", nil, nil, []string{"up, down, status, redo, to, or version", "\nTry: "}},
		{"unknown subcommand", []string{"sideways"}, nil, []string{`"sideways"`, "up, down, status, redo, to, or version", "\nTry: "}},
		{"to without version", []string{"to"}, nil, []string{"exactly one version", "\nTry: migrate to 1"}},
		{"to bad version", []string{"to", "x1"}, nil, []string{`"x1" is not a migration version`, "\nTry: "}},
		{"to negative version", []string{"to", "-1"}, nil, []string{"is not a migration version", "\nTry: "}},
		{"version with arguments", []string{"version", "2"}, nil, []string{"unexpected arguments", "\nTry: "}},
		{"extra arguments", []string{"up", "now"}, nil, []string{"unexpected arguments", "\nTry: "}},
		{
			"bad URL",
			[]string{"up"},
			[]db.MigrateOption{db.WithDatabaseURL("postgres://app:s3cr3t-pw@db:notaport/app")},
			[]string{"not valid", "\nTry: "},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := db.MigrateCommand(migrationsFS(), tt.opts...)(context.Background(), tt.args)
			if err == nil {
				t.Fatal("no error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
			if strings.Contains(err.Error(), "s3cr3t-pw") {
				t.Errorf("error %q leaks the password", err)
			}
		})
	}
}

// The command reads DATABASE_URL when no WithDatabaseURL is given.
// t.Setenv forbids t.Parallel.
func TestMigrateCommandEnvURL(t *testing.T) {
	url := testkit.URL(t, testkit.DB(t))
	t.Setenv("DATABASE_URL", url)
	var out bytes.Buffer
	if err := db.MigrateCommand(migrationsFS(), db.WithOutput(&out))(context.Background(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	if want := "pending  00001_users.sql\npending  00002_notes.sql\n"; out.String() != want {
		t.Errorf("output %q, want %q", out.String(), want)
	}

	t.Setenv("DATABASE_URL", "")
	err := db.MigrateCommand(migrationsFS())(context.Background(), []string{"status"})
	if err == nil || !strings.Contains(err.Error(), "Try: set DATABASE_URL (see .env.example)") {
		t.Errorf("empty DATABASE_URL: error = %v", err)
	}
}

func TestMigratorPending(t *testing.T) {
	t.Parallel()
	url := testkit.URL(t, testkit.DB(t))
	m, err := db.NewMigrator(url, migrationsFS(), db.WithOutput(&bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	ctx := context.Background()
	got, err := m.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "00001_users.sql,00002_notes.sql" {
		t.Errorf("Pending = %v", got)
	}
	if err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err = m.Pending(ctx); err != nil || len(got) != 0 {
		t.Errorf("Pending after Up = %v, %v", got, err)
	}
	st, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []db.MigrationStatus{{Name: "00001_users.sql", Applied: true}, {Name: "00002_notes.sql", Applied: true}}
	if len(st) != len(want) || st[0] != want[0] || st[1] != want[1] {
		t.Errorf("Status = %+v, want %+v", st, want)
	}
}

// Two runners start Up at once. The advisory lock makes one wait. Both
// succeed and each file applies exactly once.
func TestMigratorConcurrentUp(t *testing.T) {
	t.Parallel()
	pool := testkit.DB(t)
	url := testkit.URL(t, pool)
	fsys := migrationsFS()
	// A slow first migration keeps the lock held while the second runner tries.
	fsys["00001_users.sql"] = &fstest.MapFile{Data: []byte(`-- +goose Up
SELECT pg_sleep(1.5);
CREATE TABLE users (id bigint PRIMARY KEY);

-- +goose Down
DROP TABLE users;
`)}

	const runners = 2
	outs := make([]bytes.Buffer, runners)
	errs := make([]error, runners)
	var wg sync.WaitGroup
	for i := range runners {
		wg.Go(func() {
			m, err := db.NewMigrator(url, fsys, db.WithOutput(&outs[i]))
			if err != nil {
				errs[i] = err
				return
			}
			defer func() { _ = m.Close() }()
			errs[i] = m.Up(context.Background())
		})
	}
	wg.Wait()

	applied := 0
	for i := range runners {
		if errs[i] != nil {
			t.Errorf("runner %d: %v\noutput:\n%s", i, errs[i], outs[i].String())
		}
		out := outs[i].String()
		applied += strings.Count(out, "applied  ")
		switch out {
		case "migrate: 2 pending\napplied  00001_users.sql\napplied  00002_notes.sql\n",
			"migrate: no pending migrations\n",
			"migrate: 2 pending\nmigrate: another runner applied them\n":
		default:
			t.Errorf("runner %d: unexpected output:\n%s", i, out)
		}
	}
	if applied != 2 {
		t.Errorf("applied lines = %d, want 2\nrunner 0:\n%s\nrunner 1:\n%s", applied, outs[0].String(), outs[1].String())
	}
	var n int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM goose_db_version WHERE version_id > 0 AND is_applied").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("goose_db_version rows = %d, want 2", n)
	}
}

// The migration connections carry lock_timeout and statement_timeout.
func TestMigratorTimeouts(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"00001_settings.sql": {Data: []byte(`-- +goose Up
CREATE TABLE settings AS
SELECT current_setting('lock_timeout') AS lock_timeout,
       current_setting('statement_timeout') AS statement_timeout;

-- +goose Down
DROP TABLE settings;
`)},
	}
	tests := []struct {
		name          string
		opts          []db.MigrateOption
		wantLock      string
		wantStatement string
	}{
		{"defaults", nil, "5s", "5min"},
		{"options", []db.MigrateOption{
			db.WithMigrationLockTimeout(2 * time.Second),
			db.WithMigrationStatementTimeout(time.Minute),
		}, "2s", "1min"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := testkit.DB(t)
			opts := append([]db.MigrateOption{db.WithOutput(&bytes.Buffer{})}, tt.opts...)
			m, err := db.NewMigrator(testkit.URL(t, pool), fsys, opts...)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = m.Close() }()
			if err := m.Up(context.Background()); err != nil {
				t.Fatal(err)
			}
			var lock, stmt string
			if err := pool.QueryRow(context.Background(),
				"SELECT lock_timeout, statement_timeout FROM settings").Scan(&lock, &stmt); err != nil {
				t.Fatal(err)
			}
			if lock != tt.wantLock || stmt != tt.wantStatement {
				t.Errorf("lock_timeout=%q statement_timeout=%q, want %q and %q", lock, stmt, tt.wantLock, tt.wantStatement)
			}
		})
	}
}
