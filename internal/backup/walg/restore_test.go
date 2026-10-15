package walg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/backup"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

const inspection = `{"Image":"sha256:postgres","Config":{"User":"1000:1000","Env":["PGDATA=/var/lib/postgresql/18/docker","POSTGRES_USER=app","POSTGRES_DB=app"]},"Mounts":[{"Destination":"/var/lib/postgresql"}]}`

func expectInspect(r *remote.Fake) {
	r.Expect("docker", "compose").Return("postgres-id\n", "", 0)
	r.Expect("docker", "inspect").Return(inspection, "", 0)
}

func TestRestoreContainerLifecycle(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"verify", "new", "create", "start", "check", "count", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			r := &remote.Fake{}
			r.Expect("docker", "compose").Return(fixture(t), "", 0)
			expectInspect(r)
			create := r.Expect("docker", "create")
			if stage == "create" {
				create.Fail(errors.New("lost create response"))
			} else {
				start := r.Expect("docker", "start")
				if stage == "start" {
					start.Fail(context.Canceled)
				} else {
					check := r.Expect("docker", "exec").Return("2\n", "", 0)
					if stage == "check" {
						check.Fail(errors.New("recovery failed"))
					}
					if stage == "count" {
						check.Return("not a count", "", 0)
					}
				}
			}
			if stage != "new" {
				cleanup := r.Expect("sh", "-eu", "-c", cleanupScript)
				if stage == "cleanup" {
					cleanup.Fail(errors.New("cleanup failed"))
				}
			}
			b := backend(t, r)
			var err error
			if stage == "new" {
				err = b.Restore(context.Background(), backup.Target{}, backup.Into{New: true})
			} else {
				var result backup.VerifyResult
				result, err = b.Verify(context.Background(), backup.Target{})
				if result.OK != (stage == "verify") {
					t.Fatalf("verify = %+v, %v", result, err)
				}
			}
			wantErr := stage != "verify" && stage != "new"
			if (err != nil) != wantErr {
				t.Fatalf("error = %v", err)
			}
			r.AssertDone(t)
			calls := r.Calls()
			var createArgs string
			for _, c := range calls {
				if len(c.Argv) > 1 && c.Argv[1] == "create" {
					createArgs = strings.Join(c.Argv, " ")
				}
			}
			for _, want := range []string{"--network todo-backup", "type=volume,dst=/var/lib/postgresql", "/opt/todo/secrets,dst=/run/secrets,readonly", "wal-g backup-fetch", "wal-g wal-fetch", "recovery.signal", "archive_mode=off", "gosu"} {
				if !strings.Contains(createArgs, want) {
					t.Errorf("create missing %q", want)
				}
			}
			if strings.Contains(createArgs, "--publish") != (stage == "new") {
				t.Fatalf("unexpected port: %s", createArgs)
			}
		})
	}
}

func TestTargetSelection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		target  backup.Target
		want    string
		invalid bool
	}{
		{name: "label", target: backup.Target{Label: "base_000000010000000000000001"}, want: "base_000000010000000000000001"},
		{name: "time", target: backup.Target{Time: time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC)}, want: "base_000000010000000000000001"},
		{name: "too early", target: backup.Target{Time: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}},
		{name: "missing", target: backup.Target{Label: "base_000000010000000000000099"}},
		{name: "invalid", target: backup.Target{Label: "../bad"}, invalid: true},
		{name: "conflict", target: backup.Target{Label: "base_000000010000000000000001", Time: time.Now()}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &remote.Fake{}
			if !tc.invalid {
				r.Expect().Return(fixture(t), "", 0)
			}
			b, err := backend(t, r).target(context.Background(), tc.target)
			if tc.want == "" {
				if err == nil {
					t.Fatal("expected error")
				}
			} else if err != nil || b.Label != tc.want {
				t.Fatalf("target = %+v, %v", b, err)
			}
			r.AssertDone(t)
		})
	}
}

func TestRecoveryShell(t *testing.T) {
	t.Parallel()
	for _, at := range []string{"", "2026-10-15T03:04:05Z"} {
		t.Run(at, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			data := filepath.Join(dir, "data")
			if err := os.Mkdir(data, 0700); err != nil {
				t.Fatal(err)
			}
			exe := `#!/bin/sh
set -eu
[ "$1" = backup-fetch ] && [ "$2" = "$PGDATA" ] && [ "$3" = base_000000010000000000000001 ]
printf "shared_buffers = '128MB'\nrecovery_target_time = 'old'\nrestore_command = 'old'\n" > "$PGDATA/postgresql.auto.conf"
touch "$PGDATA/standby.signal"
`
			if err := os.WriteFile(filepath.Join(dir, "wal-g"), []byte(exe), 0700); err != nil {
				t.Fatal(err)
			}
			_, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "sh", Args: []string{"-eu", "-c", recoveryScript, "walg", "base_000000010000000000000001", at}, Env: []string{"PATH=" + dir + ":" + os.Getenv("PATH"), "PGDATA=" + data}})
			if err != nil {
				t.Fatal(err)
			}
			out, err := os.ReadFile(filepath.Join(data, "postgresql.auto.conf"))
			if err != nil {
				t.Fatal(err)
			}
			s := string(out)
			for _, want := range []string{"shared_buffers = '128MB'", `restore_command = 'wal-g wal-fetch "%f" "%p"'`, "recovery_target_action = 'promote'"} {
				if !strings.Contains(s, want) {
					t.Errorf("missing %q: %s", want, s)
				}
			}
			if strings.Contains(s, "old") {
				t.Fatal(s)
			}
			target := "recovery_target = 'immediate'"
			if at != "" {
				target = "recovery_target_time = '" + at + "'"
			}
			if !strings.Contains(s, target) {
				t.Fatal(s)
			}
			if _, err := os.Stat(filepath.Join(data, "recovery.signal")); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(data, "standby.signal")); !os.IsNotExist(err) {
				t.Fatalf("standby.signal remains: %v", err)
			}
		})
	}
}

func TestInPlaceRestore(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "fetch failure"}[fail], func(t *testing.T) {
			t.Parallel()
			r := &remote.Fake{}
			r.Expect().Return(fixture(t), "", 0)
			expectInspect(r)
			r.Expect("docker", "compose") // stop
			fetch := r.Expect("docker", "compose")
			if fail {
				fetch.Fail(errors.New("fetch failed"))
			} else {
				r.Expect("docker", "compose") // start
				r.Expect("docker", "compose").Return("1\n", "", 0)
			}
			at := time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC)
			err := backend(t, r).Restore(context.Background(), backup.Target{Time: at}, backup.Into{InPlace: true})
			if (err != nil) != fail {
				t.Fatalf("restore = %v", err)
			}
			r.AssertDone(t)
			args := strings.Join(r.Calls()[4].Argv, " ")
			for _, want := range []string{"compose.backups-walg.yml", ".spinup-before-restore", "wal-g backup-fetch", at.Format(time.RFC3339)} {
				if !strings.Contains(args, want) {
					t.Errorf("missing %q", want)
				}
			}
			if fail && !strings.Contains(err.Error(), "previous data remains") {
				t.Fatal(err)
			}
		})
	}
}
