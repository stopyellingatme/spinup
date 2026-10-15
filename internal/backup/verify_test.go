package backup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/remote"
)

func expectRestoreContainer(t *testing.T, vm *remote.Fake, verify, fail bool) {
	t.Helper()
	purpose := "restore"
	if verify {
		purpose = "verify"
	}
	name := "todo-" + purpose + "-" + restoreLabel
	vm.Expect("docker", "ps", "-aq").Return("postgres-id", "", 0)
	vm.Expect("docker", "inspect", "--format", "{{json .}}", "postgres-id").Return(`{"Config":{"Image":"todo-postgres:pinned","User":"1000:1000","Env":["PGDATA=/var/lib/postgresql/18/docker"]},"Mounts":[{"Destination":"/var/lib/postgresql"}]}`, "", 0)
	vm.Expect("docker", "create", "--name", name).Return("restore-id", "", 0)
	vm.Expect("docker", "start", name).Return("", "", 0)
	ready := vm.Expect("docker", "exec", name, "sh", "-c")
	if fail {
		ready.Return("", "query failed", 1)
	} else {
		ready.Return("2\n", "", 0)
	}
	if verify || fail {
		vm.Expect("docker", "rm", "-fv", name).Return("", "", 0)
	}
}

func TestPGBackRestVerifyCleanup(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "query failure"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			vm := &remote.Fake{}
			expectRestoreContainer(t, vm, true, fail)
			result, err := (&PGBackRest{App: "todo", Remote: vm}).Verify(context.Background(), Target{Label: restoreLabel})
			if (err != nil) != fail || result.OK == fail || result.Label != restoreLabel {
				t.Fatalf("verify: %+v %v", result, err)
			}
			vm.AssertDone(t)
			calls := vm.Calls()
			create := strings.Join(calls[2].Argv, " ")
			for _, want := range []string{"--network todo-backup", "todo-postgres:pinned", "dst=/run/secrets,readonly", "dst=/var/lib/pgbackrest,readonly", "archive_mode=off", "restore_command=", "--set=" + restoreLabel} {
				if !strings.Contains(create, want) {
					t.Errorf("create omitted %s: %s", want, create)
				}
			}
			if strings.Contains(create, "--publish") {
				t.Fatal("verify published a port")
			}
			ready := strings.Join(calls[4].Argv, " ")
			for _, want := range []string{"pg_isready", "pg_is_in_recovery()", "SELECT count(*) FROM goose_db_version"} {
				if !strings.Contains(ready, want) {
					t.Errorf("missing %s", want)
				}
			}
		})
	}
}

func TestPGBackRestCleanupAfterStartFailure(t *testing.T) {
	t.Parallel()
	vm := &remote.Fake{}
	vm.Expect("docker", "ps", "-aq").Return("postgres-id", "", 0)
	vm.Expect("docker", "inspect").Return(`{"Config":{"Image":"todo-postgres:pinned","User":"1000:1000","Env":["PGDATA=/var/lib/postgresql/data/pgdata"]},"Mounts":[{"Destination":"/var/lib/postgresql/data"}]}`, "", 0)
	vm.Expect("docker", "create").Return("id", "", 0)
	vm.Expect("docker", "start").Return("", "start failed", 1)
	vm.Expect("docker", "rm", "-fv", "todo-restore-"+restoreLabel).Return("", "", 0)
	err := (&PGBackRest{App: "todo", Remote: vm}).Restore(context.Background(), Target{Label: restoreLabel}, Into{New: true})
	if err == nil {
		t.Fatal("start failure was lost")
	}
	vm.AssertDone(t)
}

func TestPGBackRestRestoreCollisionPreservesExisting(t *testing.T) {
	t.Parallel()
	vm := &remote.Fake{}
	vm.Expect("docker", "ps", "-aq").Return("postgres-id", "", 0)
	vm.Expect("docker", "inspect").Return(`{"Config":{"Image":"image","User":"1000:1000","Env":["PGDATA=/var/lib/postgresql/18/docker"]},"Mounts":[{"Destination":"/var/lib/postgresql"}]}`, "", 0)
	vm.Expect("docker", "create").Return("", "name already in use", 1)
	err := (&PGBackRest{App: "todo", Remote: vm}).Restore(context.Background(), Target{Label: restoreLabel}, Into{New: true})
	if err == nil {
		t.Fatal("collision was lost")
	}
	vm.AssertDone(t)
	if len(vm.Calls()) != 3 {
		t.Fatal("removed an existing container after collision")
	}
}

func TestTargets(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		label, at string
		bad       bool
	}{
		{restoreLabel, "", false}, {"", "2026-10-15T03:00:00Z", false}, {"", "yesterday", true}, {restoreLabel, "2026-10-15T03:00:00Z", true},
	} {
		_, err := ParseTarget(tt.label, tt.at)
		if (err != nil) != tt.bad {
			t.Errorf("ParseTarget(%q,%q): %v", tt.label, tt.at, err)
		}
	}
	p := &PGBackRest{App: "todo", Remote: &remote.Fake{}}
	if _, err := p.Verify(context.Background(), Target{Label: "../../bad"}); err == nil {
		t.Fatal("accepted invalid physical label")
	}
	if err := p.Restore(context.Background(), Target{Label: restoreLabel}, Into{}); err == nil {
		t.Fatal("accepted missing destination")
	}
	args := strings.Join(restoreArgs("todo", Target{Time: time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)}), " ")
	if !strings.Contains(args, "--type=time --target=2026-10-15T03:00:00Z") {
		t.Fatal(args)
	}
}

func TestVerifyJSONAndFailure(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		vm := &remote.Fake{}
		expectRestoreContainer(t, vm, true, fail)
		var out strings.Builder
		result, err := Verify(context.Background(), Options{Dir: newProject(t, true, "", nil), Env: "staging", Remote: vm, Out: &out, JSON: true}, Target{Label: restoreLabel})
		if (err != nil) != fail || result.OK == fail || !strings.Contains(out.String(), `"label": "`+restoreLabel+`"`) {
			t.Fatalf("JSON verify: %s %+v %v", out.String(), result, err)
		}
		vm.AssertDone(t)
	}
}
