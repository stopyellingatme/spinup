package deploy

import (
	"context"
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
		{name: "empty table", table: "0\n"},
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
			f := &flow{vm: vm, l: &local{app: "todo", migrations: []migration{{1, "00001_init.sql"}, {4, "00004_drop.sql"}, {6, "00006_applied.sql"}}}}
			got, err := f.pendingOnVM(context.Background(), tt.current)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("pending = %v, %v, want %v", got, err, tt.want)
			}
			vm.AssertDone(t)
		})
	}
}
