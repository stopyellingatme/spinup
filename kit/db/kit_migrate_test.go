package db_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/db"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

func kitFiles() fstest.MapFS {
	return fstest.MapFS{
		"00002_create_auth.sql":        {Data: []byte("-- +goose Up\nCREATE TABLE users (id bigint PRIMARY KEY);\nCREATE TABLE sessions (token text PRIMARY KEY);\n-- +goose Down\nDROP TABLE sessions;\nDROP TABLE users;\n")},
		"00005_users_role.sql":         {Data: []byte("-- +goose Up\nALTER TABLE users ADD COLUMN role text;\n-- +goose Down\nALTER TABLE users DROP COLUMN role;\n")},
		"00007_users_auth_version.sql": {Data: []byte("-- +goose Up\nALTER TABLE users ADD COLUMN auth_version bigint;\nALTER TABLE sessions ADD COLUMN user_id bigint REFERENCES users(id);\n-- +goose Down\nALTER TABLE sessions DROP COLUMN user_id;\nALTER TABLE users DROP COLUMN auth_version;\n")},
	}
}

func appFiles() fstest.MapFS {
	return fstest.MapFS{
		"00001_init.sql":         {Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")},
		"00003_create_todos.sql": {Data: []byte("-- +goose Up\nCREATE TABLE todos (id bigint PRIMARY KEY, user_id bigint REFERENCES users(id));\n-- +goose Down\nDROP TABLE todos;\n")},
	}
}

func kitCmd(t *testing.T, url string, app, kit fstest.MapFS, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	cmd := db.MigrateCommand(app, db.WithKitMigrations(kit), db.WithDatabaseURL(url), db.WithOutput(&out))
	if err := cmd(t.Context(), args); err != nil {
		t.Fatalf("migrate %v: %v\n%s", args, err, out.String())
	}
	return out.String()
}

func seedLegacy(t *testing.T, pool *pgxpool.Pool, app, kit fstest.MapFS) {
	t.Helper()
	legacy := fstest.MapFS{}
	for name, data := range app {
		legacy[name] = data
	}
	legacy["00002_create_auth.sql"] = kit["00002_create_auth.sql"]
	runCmd(t, testkit.URL(t, pool), legacy, "up")
}

func TestKitMigrationAdoptionTodo(t *testing.T) {
	t.Parallel()
	pool := testkit.DB(t)
	url := testkit.URL(t, pool)
	app, kit := appFiles(), kitFiles()
	seedLegacy(t, pool, app, kit)
	want := "migrate: 2 pending\napplied  kit 00005_users_role.sql\napplied  kit 00007_users_auth_version.sql\n"
	if got := kitCmd(t, url, app, kit, "up"); got != want {
		t.Fatalf("up = %q, want %q", got, want)
	}
	want = "applied  kit 00002_create_auth.sql\napplied  kit 00005_users_role.sql\napplied  kit 00007_users_auth_version.sql\napplied  00001_init.sql\napplied  00003_create_todos.sql\n"
	if got := kitCmd(t, url, app, kit, "status"); got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if got := kitCmd(t, url, app, kit, "up"); got != "migrate: no pending migrations\n" {
		t.Fatal(got)
	}
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM goose_db_version WHERE version_id = 2").Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy row count = %d, %v", count, err)
	}
	if got := kitCmd(t, url, app, kit, "down"); got != "rolled back  00003_create_todos.sql\n" {
		t.Fatal(got)
	}
}

func TestKitMigrationNewDatabase(t *testing.T) {
	t.Parallel()
	pool := testkit.DB(t)
	url := testkit.URL(t, pool)
	app, kit := appFiles(), kitFiles()
	var out bytes.Buffer
	hook := db.Hook{AfterUp: func(ctx context.Context, _ string) error {
		var tables int
		return pool.QueryRow(ctx, "SELECT count(*) FROM todos").Scan(&tables)
	}}
	cmd := db.MigrateCommand(app, db.WithKitMigrations(kit), db.WithDatabaseURL(url), db.WithOutput(&out), db.WithHook(hook))
	if err := cmd(t.Context(), []string{"up"}); err != nil {
		t.Fatal(err)
	}
	want := "migrate: 5 pending\napplied  kit 00002_create_auth.sql\napplied  kit 00005_users_role.sql\napplied  kit 00007_users_auth_version.sql\napplied  00001_init.sql\napplied  00003_create_todos.sql\n"
	if out.String() != want {
		t.Fatalf("up = %q, want %q", out.String(), want)
	}
	if got := kitCmd(t, url, app, kit, "version"); got != "3\n" {
		t.Fatal(got)
	}
}

func TestKitMigrationLifecycle(t *testing.T) {
	t.Parallel()
	url := testkit.URL(t, testkit.DB(t))
	app, kit := appFiles(), kitFiles()
	steps := []struct {
		args []string
		want string
	}{
		{[]string{"status"}, "pending  kit 00002_create_auth.sql\npending  kit 00005_users_role.sql\npending  kit 00007_users_auth_version.sql\n"},
		{[]string{"version"}, "0\n"},
		{[]string{"up"}, "migrate: 3 pending\napplied  kit 00002_create_auth.sql\napplied  kit 00005_users_role.sql\napplied  kit 00007_users_auth_version.sql\n"},
		{[]string{"status"}, "applied  kit 00002_create_auth.sql\napplied  kit 00005_users_role.sql\napplied  kit 00007_users_auth_version.sql\n"},
		{[]string{"version"}, "7\n"},
		{[]string{"down"}, "rolled back  kit 00007_users_auth_version.sql\n"},
		{[]string{"to", "2"}, "rolled back  kit 00005_users_role.sql\n"},
		{[]string{"to", "7"}, "applied  kit 00005_users_role.sql\napplied  kit 00007_users_auth_version.sql\n"},
		{[]string{"to", "7"}, "migrate: already at version 7\n"},
		{[]string{"to", "0"}, "rolled back  kit 00007_users_auth_version.sql\nrolled back  kit 00005_users_role.sql\nrolled back  kit 00002_create_auth.sql\n"},
		{[]string{"down"}, "migrate: nothing to roll back\n"},
	}
	for _, step := range steps {
		args := append([]string{"kit"}, step.args...)
		if got := kitCmd(t, url, app, kit, args...); got != step.want {
			t.Fatalf("migrate %v = %q, want %q", args, got, step.want)
		}
	}
	if got := kitCmd(t, url, app, kit, "version"); got != "0\n" {
		t.Fatalf("app version = %q", got)
	}
}

func TestKitMigrationConcurrentAdoption(t *testing.T) {
	t.Parallel()
	pool := testkit.DB(t)
	app, kit := appFiles(), kitFiles()
	seedLegacy(t, pool, app, kit)
	var outputs [2]bytes.Buffer
	var errs [2]error
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Go(func() {
			m, err := db.NewMigrator(testkit.URL(t, pool), app, db.WithKitMigrations(kit), db.WithOutput(&outputs[i]))
			if err != nil {
				errs[i] = err
				return
			}
			defer func() { _ = m.Close() }()
			<-start
			_, errs[i] = m.KitStatus(t.Context())
		})
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, out := range outputs {
		if out.Len() != 0 {
			t.Fatalf("adoption wrote to stdout: %q", out.String())
		}
	}
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+db.KitVersionTable+" WHERE version_id = 2").Scan(&count); err != nil || count != 1 {
		t.Fatalf("kit rows = %d, %v", count, err)
	}
}

func TestKitMigrationProbeRollback(t *testing.T) {
	t.Parallel()
	pool := testkit.DB(t)
	app, kit := appFiles(), kitFiles()
	seedLegacy(t, pool, app, kit)
	if _, err := pool.Exec(t.Context(), "INSERT INTO goose_db_version (version_id, is_applied) VALUES (5, true)"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := db.MigrateCommand(app, db.WithKitMigrations(kit), db.WithDatabaseURL(testkit.URL(t, pool)), db.WithOutput(&out))(t.Context(), []string{"up"})
	want := "migrate: row 5 in goose_db_version is not kit migration 00005_users_role.sql.\nTry: renumber the app migration row by hand (UPDATE goose_db_version SET version_id = <app number> WHERE version_id = 5), then run migrate up again."
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
	if out.Len() != 0 {
		t.Fatalf("rolled-back adoption output = %q", out.String())
	}
	var legacy int
	var kitExists bool
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM goose_db_version WHERE version_id IN (2, 5)").Scan(&legacy); err != nil || legacy != 2 {
		t.Fatalf("legacy rows = %d, %v", legacy, err)
	}
	if err := pool.QueryRow(t.Context(), "SELECT to_regclass('public.spinup_kit_version') IS NOT NULL").Scan(&kitExists); err != nil || kitExists {
		t.Fatalf("kit exists = %t, %v", kitExists, err)
	}
}

func TestKitMigrationNoOption(t *testing.T) {
	t.Parallel()
	err := db.MigrateCommand(appFiles())(t.Context(), []string{"kit", "status"})
	want := "migrate kit: this app has no kit migrations\nTry: add a module with spinup module add auth"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
}

func TestKitMigrationEmptyAndInvalidFS(t *testing.T) {
	t.Parallel()
	m, err := db.NewMigrator("postgres://localhost/app", fstest.MapFS{}, db.WithKitMigrations(fstest.MapFS{}), db.WithOutput(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	if m.KitHasVersion(2) || m.HasVersion(2) {
		t.Fatal("empty filesystem has a version")
	}
	if statuses, err := m.KitStatus(t.Context()); err != nil || len(statuses) != 0 {
		t.Fatalf("empty kit status = %v, %v", statuses, err)
	}
	_, err = db.NewMigrator("postgres://localhost/app", appFiles(), db.WithKitMigrations(fstest.MapFS{
		"00002_first.sql":     {Data: []byte("-- +goose Up\nSELECT 1;\n")},
		"00002_duplicate.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n")},
	}))
	if err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("duplicate kit version: %v", err)
	}
}

func TestKitMigrationAppOwnsVersion(t *testing.T) {
	t.Parallel()
	pool := testkit.DB(t)
	app := migrationsFS()
	runCmd(t, testkit.URL(t, pool), app, "up")
	kit := fstest.MapFS{"00002_kit_notes.sql": {Data: []byte("-- +goose Up\nCREATE TABLE kit_notes (id bigint);\n-- +goose Down\nDROP TABLE kit_notes;\n")}}
	if got := kitCmd(t, testkit.URL(t, pool), app, kit, "up"); got != "migrate: 1 pending\napplied  kit 00002_kit_notes.sql\n" {
		t.Fatal(got)
	}
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM goose_db_version WHERE version_id = 2").Scan(&count); err != nil || count != 1 {
		t.Fatalf("app rows = %d, %v", count, err)
	}
}

func TestKitMigrationInvalidCommands(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"kit"}, {"kit", "redo"}, {"kit", "up", "extra"}, {"kit", "to"}, {"kit", "to", "-1"}} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			err := db.MigrateCommand(appFiles(), db.WithKitMigrations(kitFiles()))(t.Context(), args)
			if err == nil || !strings.Contains(err.Error(), "Try: migrate kit") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestKitMigrationLegacyProbes(t *testing.T) {
	t.Parallel()
	kit := kitFiles()
	for version, table := range map[int]string{3: "password_resets", 4: "user_identities", 6: "audit_events"} {
		name := fmt.Sprintf("%05d_%s.sql", version, table)
		kit[name] = &fstest.MapFile{Data: []byte(fmt.Sprintf("-- +goose Up\nCREATE TABLE %s (id bigint);\n-- +goose Down\nDROP TABLE %s;\n", table, table))}
	}
	app := appFiles()
	delete(app, "00003_create_todos.sql")
	for _, missing := range []int{0, 2, 3, 4, 5, 6, 7} {
		t.Run(fmt.Sprintf("missing %d", missing), func(t *testing.T) {
			t.Parallel()
			pool := testkit.DB(t)
			legacy := fstest.MapFS{}
			for name, file := range app {
				legacy[name] = file
			}
			for name, file := range kit {
				legacy[name] = file
			}
			runCmd(t, testkit.URL(t, pool), legacy, "up")
			drops := map[int]string{
				2: "DROP TABLE users CASCADE",
				3: "DROP TABLE password_resets",
				4: "DROP TABLE user_identities",
				5: "ALTER TABLE users DROP COLUMN role",
				6: "DROP TABLE audit_events",
				7: "ALTER TABLE users DROP COLUMN auth_version",
			}
			if missing != 0 {
				if _, err := pool.Exec(t.Context(), drops[missing]); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			m, err := db.NewMigrator(testkit.URL(t, pool), app, db.WithKitMigrations(kit), db.WithOutput(&out))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = m.Close() }()
			_, err = m.Status(t.Context())
			if missing != 0 {
				if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("row %d in goose_db_version is not kit migration", missing)) {
					t.Fatalf("missing probe %d: %v", missing, err)
				}
				if out.Len() != 0 {
					t.Fatalf("failed adoption output = %q", out.String())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if out.Len() != 0 {
				t.Fatalf("adoption wrote to stdout: %q", out.String())
			}
			list, err := m.KitStatus(t.Context())
			if err != nil || len(list) != 6 {
				t.Fatalf("KitStatus = %v, %v", list, err)
			}
			for _, status := range list {
				if !status.Applied {
					t.Fatalf("pending: %s", status.Name)
				}
			}
		})
	}
}

func TestKitMigrationDoesNotAdoptUnappliedOrNewVersions(t *testing.T) {
	t.Parallel()
	pool := testkit.DB(t)
	app := appFiles()
	delete(app, "00003_create_todos.sql")
	runCmd(t, testkit.URL(t, pool), app, "up")
	if _, err := pool.Exec(t.Context(), "INSERT INTO goose_db_version (version_id, is_applied) VALUES (5, false), (8, true)"); err != nil {
		t.Fatal(err)
	}
	kit := kitFiles()
	kit["00008_future.sql"] = &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")}
	got := kitCmd(t, testkit.URL(t, pool), app, kit, "status")
	if strings.Contains(got, "adopted") || strings.Contains(got, "applied  kit") {
		t.Fatal(got)
	}
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM goose_db_version WHERE version_id IN (5, 8)").Scan(&count); err != nil || count != 2 {
		t.Fatalf("app rows = %d, %v", count, err)
	}
}

func TestKitMigratorAPI(t *testing.T) {
	t.Parallel()
	pool := testkit.DB(t)
	var out bytes.Buffer
	m, err := db.NewMigrator(testkit.URL(t, pool), appFiles(), db.WithKitMigrations(kitFiles()), db.WithOutput(&out))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	ctx := t.Context()
	if !m.KitHasVersion(5) || m.KitHasVersion(3) || m.HasVersion(5) {
		t.Fatal("versions crossed spaces")
	}
	if err := m.KitUpTo(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if got, err := m.KitVersion(ctx); err != nil || got != 5 {
		t.Fatalf("KitVersion = %d, %v", got, err)
	}
	if err := m.KitDownTo(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err := m.KitUp(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.KitDown(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := m.KitVersion(ctx); err != nil || got != 5 {
		t.Fatalf("KitVersion = %d, %v", got, err)
	}
	if pending, err := m.Pending(ctx); err != nil || len(pending) != 2 {
		t.Fatalf("Pending = %v, %v", pending, err)
	}
	if status, err := m.Status(ctx); err != nil || len(status) != 2 {
		t.Fatalf("Status = %v, %v", status, err)
	}
	want := "applied  kit 00002_create_auth.sql\napplied  kit 00005_users_role.sql\nrolled back  kit 00005_users_role.sql\nmigrate: 2 pending\napplied  kit 00005_users_role.sql\napplied  kit 00007_users_auth_version.sql\nrolled back  kit 00007_users_auth_version.sql\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestKitCommandEmptySpaceSkipsHooks(t *testing.T) {
	t.Parallel()
	for _, kit := range []fs.FS{nil, fstest.MapFS{}} {
		var out bytes.Buffer
		calls := 0
		hook := func(context.Context, string) error { calls++; return nil }
		cmd := db.MigrateCommand(fstest.MapFS{}, db.WithDatabaseURL("postgres://localhost/app"), db.WithKitMigrations(kit), db.WithOutput(&out), db.WithHook(db.Hook{AfterUp: hook, BeforeDown: hook}))
		for _, args := range [][]string{{"kit", "up"}, {"kit", "down"}, {"kit", "status"}, {"kit", "to", "0"}, {"kit", "version"}} {
			if err := cmd(t.Context(), args); err != nil {
				t.Fatalf("%v: %v", args, err)
			}
		}
		if calls != 0 {
			t.Fatalf("kit command ran %d app hooks", calls)
		}
		want := "migrate: no pending migrations\nmigrate: nothing to roll back\nmigrate: already at version 0\n0\n"
		if out.String() != want {
			t.Fatalf("empty kit output = %q, want %q", out.String(), want)
		}
	}
}

func TestKitMigrationVersionAfterAdoption(t *testing.T) {
	t.Parallel()
	pool := testkit.DB(t)
	app, kit := appFiles(), kitFiles()
	seedLegacy(t, pool, app, kit)
	if got := kitCmd(t, testkit.URL(t, pool), app, kit, "version"); got != "3\n" {
		t.Fatalf("version stdout = %q", got)
	}
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM spinup_kit_version WHERE version_id = 2 AND is_applied").Scan(&count); err != nil || count != 1 {
		t.Fatalf("adopted rows = %d, error = %v", count, err)
	}
}

func TestKitMigrationProbeRequiresSessionsUserID(t *testing.T) {
	t.Parallel()
	pool := testkit.DB(t)
	app, kit := appFiles(), kitFiles()
	legacy := fstest.MapFS{}
	for name, file := range app {
		legacy[name] = file
	}
	for name, file := range kit {
		legacy[name] = file
	}
	url := testkit.URL(t, pool)
	runCmd(t, url, legacy, "up")
	if _, err := pool.Exec(t.Context(), "ALTER TABLE sessions DROP COLUMN user_id"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := db.MigrateCommand(app, db.WithKitMigrations(kit), db.WithDatabaseURL(url), db.WithOutput(&out))(t.Context(), []string{"version"})
	if err == nil || !strings.Contains(err.Error(), "row 7 in goose_db_version is not kit migration") {
		t.Fatalf("partial version 7: %v", err)
	}
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM goose_db_version WHERE version_id IN (2, 5, 7)").Scan(&count); err != nil || count != 3 {
		t.Fatalf("legacy rows after rollback = %d, error = %v", count, err)
	}
	if out.Len() != 0 {
		t.Fatalf("failed adoption stdout = %q", out.String())
	}
}
