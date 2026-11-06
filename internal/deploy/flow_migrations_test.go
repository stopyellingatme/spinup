package deploy

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/remote"
)

func migrationStatusArgv() []string {
	return []string{"docker", "compose", "-p", "todo", "ps", "--status", "running", "-q", "postgres"}
}

func migrationQuery(query string) []string {
	return []string{"docker", "compose", "-p", "todo", "exec", "-T", "postgres", "psql", "-U", "app", "-d", "app", "-X", "-q", "-t", "-A", "-v", "ON_ERROR_STOP=1", "-c", query}
}

func expectAppliedMigrations(vm *remote.Fake, applied string) {
	vm.Expect(migrationStatusArgv()...).Return("postgres\n", "", 0)
	vm.Expect(migrationQuery(gooseTableQuery)...).Return("0\n", "", 0)
	vm.Expect(migrationQuery(gooseAppliedQuery)...).Return(applied, "", 0)
}

func TestAppliedMigrations(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		table string
		rows  string
		code  int
		want  []int64
		err   string
	}{
		{name: "complete set with a gap", table: "0\n", rows: "0\n1\n6\n", want: []int64{0, 1, 6}},
		{name: "missing table", table: "-1\n"},
		{name: "empty table", table: "0\n", want: []int64{}},
		{name: "invalid version", table: "0\n", rows: "1\ninvalid\n6\n", err: "parse the applied migration version"},
		{name: "query failure", table: "0\n", code: 1, err: "query the applied migrations"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			vm := &remote.Fake{}
			vm.Expect(migrationQuery(gooseTableQuery)...).Return(tt.table, "", 0)
			if strings.TrimSpace(tt.table) != "-1" {
				vm.Expect(migrationQuery(gooseAppliedQuery)...).Return(tt.rows, "query failed", tt.code)
			}
			got, err := AppliedMigrations(context.Background(), vm, "todo")
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) || !strings.Contains(err.Error(), "Try:") {
					t.Fatalf("err = %v, want %q and a Try line", err, tt.err)
				}
			} else if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("applied = %v, %v, want %v", got, err, tt.want)
			}
			vm.AssertDone(t)
		})
	}
}

func TestPendingOnVM(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		current *Release
		running bool
		applied string
		want    []string
	}{
		{"first deploy", nil, false, "", []string{"00001_init.sql", "00004_drop.sql", "00006_applied.sql"}},
		{"out of order", &Release{MigrationVersion: "6"}, true, "1\n6\n", []string{"00004_drop.sql"}},
		{"all applied", &Release{MigrationVersion: "6"}, true, "1\n4\n6\n", nil},
		{"stopped database", &Release{MigrationVersion: "6"}, false, "", []string{"00001_init.sql", "00004_drop.sql", "00006_applied.sql"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			vm := &remote.Fake{}
			if tt.current != nil {
				if tt.running {
					expectAppliedMigrations(vm, tt.applied)
				} else {
					vm.Expect(migrationStatusArgv()...).Return("", "", 0)
				}
			}
			f := &flow{vm: vm, l: &local{app: "todo", migrations: []migration{{Version: 1, Name: "00001_init.sql"}, {Version: 4, Name: "00004_drop.sql"}, {Version: 6, Name: "00006_applied.sql"}}}}
			got, err := f.pendingOnVM(context.Background(), tt.current)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("pending = %v, %v, want %v", got, err, tt.want)
			}
			vm.AssertDone(t)
		})
	}
}

func TestMigrationSpaces(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"api/db/kit/00008_flags.sql":        "-- +goose Up\nSELECT 1;",
		"api/db/kit/00002_auth.sql":         "-- +goose Up\nSELECT 1;",
		"api/db/migrations/00002_notes.sql": "-- +goose Up\nSELECT 1;",
	})
	ms, err := migrations(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 3 || ms[0].Name != "00002_auth.sql" || ms[1].Name != "00008_flags.sql" {
		t.Fatalf("migrations = %+v", ms)
	}
}

func TestKitPendingOnVM(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, table, rows string
		appMissing        bool
		code              int
		want              []string
		wantErr           bool
	}{
		{name: "separate versions", table: "0\n", rows: "2\n", want: []string{"kit 00008_flags.sql"}},
		{name: "applied kit cannot hide pending app", table: "0\n", rows: "2\n8\n", appMissing: true, want: []string{"app 00002_notes.sql"}},
		{name: "absent kit table", table: "-1\n", want: []string{"kit 00002_auth.sql", "kit 00008_flags.sql"}},
		{name: "unparseable kit row", table: "0\n", rows: "bad\n", wantErr: true},
		{name: "kit query fails", table: "0\n", code: 1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vm := &remote.Fake{}
			appRows := "2\n8\n"
			if tc.appMissing {
				appRows = ""
			}
			expectAppliedMigrations(vm, appRows)
			vm.Expect(migrationQuery(kitTableQuery)...).Return(tc.table, "", 0)
			if tc.table != "-1\n" {
				vm.Expect(migrationQuery(kitAppliedQuery)...).Return(tc.rows, "failed", tc.code)
			}
			f := &flow{vm: vm, l: &local{app: "todo", migrations: []migration{
				{Space: "kit", Version: 2, Name: "00002_auth.sql"},
				{Space: "kit", Version: 8, Name: "00008_flags.sql"},
				{Space: "app", Version: 2, Name: "00002_notes.sql"},
			}}}
			got, err := f.pendingOnVM(t.Context(), &Release{})
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "Try:") {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("pending = %v, %v, want %v", got, err, tc.want)
			}
			vm.AssertDone(t)
		})
	}
}

func TestKitMigrationJournal(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	writeTree(t, x.root, map[string]string{"api/db/kit/00008_flags.sql": "-- +goose Up\nSELECT 1;"})
	x.expectFirstDeploy(nil, func() { x.expectSSHTransport("todo-api:" + testRelease) })
	if err := x.run(); err != nil {
		t.Fatal(err)
	}
	x.vm.AssertDone(t)
	x.exec.AssertDone(t)
	var release Release
	var found bool
	for _, upload := range x.vm.Uploads() {
		if strings.HasSuffix(upload.Path, "/release.json") {
			if err := json.Unmarshal(upload.Data, &release); err != nil {
				t.Fatal(err)
			}
			found = true
		}
	}
	want := "kit 00008_flags.sql, app 00001_init.sql, app 00002_more.sql"
	if !found || release.MigrationVersion != want {
		t.Fatalf("release = %+v", release)
	}
	entries := x.journalEntries()
	var journaled bool
	for _, entry := range entries {
		if entry.Step == "migrate" && entry.Status == "done" {
			journaled = true
			if entry.Detail != want {
				t.Fatalf("detail = %q", entry.Detail)
			}
		}
	}
	if !journaled {
		t.Fatal("migration journal entry missing")
	}
}

func TestLintMigrationSpaces(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"api/db/kit/00008_drop.sql":        "-- +goose Up\nDROP TABLE users;",
		"api/db/migrations/00002_drop.sql": "-- +goose Up\nDROP TABLE notes;",
	})
	got, err := DefaultDeps().Lint(root)
	if err != nil || len(got) != 2 || got[0].File != "00008_drop.sql" || got[1].File != "00002_drop.sql" {
		t.Fatalf("lint = %+v, %v", got, err)
	}
}

func TestPendingOnVMLegacyKitAdoption(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, table string
		collide     bool
		want        []string
	}{
		{name: "project legacy rows", table: "-1\n", want: []string{"kit 00008_flags.sql"}},
		{name: "app owns legacy version", table: "-1\n", collide: true, want: []string{"kit 00002_auth.sql", "kit 00008_flags.sql"}},
		{name: "existing empty kit table", table: "0\n", want: []string{"kit 00002_auth.sql", "kit 00007_auth_version.sql", "kit 00008_flags.sql"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vm := &remote.Fake{}
			expectAppliedMigrations(vm, "1\n2\n7\n8\n")
			vm.Expect(migrationQuery(kitTableQuery)...).Return(tc.table, "", 0)
			if tc.table != "-1\n" {
				vm.Expect(migrationQuery(kitAppliedQuery)...).Return("", "", 0)
			}
			ms := []migration{{Space: "kit", Version: 2, Name: "00002_auth.sql"}, {Space: "kit", Version: 7, Name: "00007_auth_version.sql"}, {Space: "kit", Version: 8, Name: "00008_flags.sql"}, {Space: "app", Version: 1, Name: "00001_init.sql"}}
			if tc.collide {
				ms = append(ms, migration{Space: "app", Version: 2, Name: "00002_notes.sql"})
			}
			f := &flow{vm: vm, l: &local{app: "todo", migrations: ms}}
			got, err := f.pendingOnVM(t.Context(), &Release{})
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("pending = %v, error = %v, want %v", got, err, tc.want)
			}
			vm.AssertDone(t)
		})
	}
}

func TestLintAppIndexOnKitTable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"api/db/kit/00002_auth.sql":         "-- +goose Up\nCREATE TABLE users (id bigint);\nCREATE INDEX users_initial ON users(id);",
		"api/db/migrations/00001_index.sql": "-- +goose Up\nCREATE INDEX users_later ON users(id);",
	})
	got, err := LintMigrations(root)
	if err != nil || len(got) != 1 || got[0].File != "00001_index.sql" || got[0].Rule != "index-not-concurrent" {
		t.Fatalf("lint = %+v, error = %v", got, err)
	}
}
