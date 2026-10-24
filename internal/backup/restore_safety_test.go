package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/deploy"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestPGBackRestMissingTargetDoesNotStopPostgres(t *testing.T) {
	t.Parallel()
	for _, target := range []Target{{Label: "20261015-010101F"}, {Time: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)}} {
		t.Run(target.Label+target.Time.String(), func(t *testing.T) {
			t.Parallel()
			vm := &remote.Fake{}
			vm.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
			err := (&PGBackRest{App: "todo", Remote: vm}).Restore(context.Background(), target, Into{InPlace: true})
			if err == nil || !strings.Contains(err.Error(), "select pgBackRest backup") {
				t.Fatalf("target error: %v", err)
			}
			vm.AssertDone(t)
			if len(vm.Calls()) != 1 {
				t.Fatalf("missing target caused commands: %+v", vm.Calls())
			}
		})
	}
}

func TestVerifyInvalidTargetPreservesMetrics(t *testing.T) {
	t.Parallel()
	vm := &remote.Fake{}
	var warnings strings.Builder
	_, err := Verify(context.Background(), Options{Dir: newProject(t, true, "", nil), Env: "staging", Remote: vm, ErrOut: &warnings}, Target{Label: "typo"})
	if err == nil {
		t.Fatal("accepted typo")
	}
	if len(vm.Calls()) != 0 || warnings.Len() != 0 {
		t.Fatalf("invalid target attempted metrics: %+v %s", vm.Calls(), warnings.String())
	}
}

func restoreProject(t *testing.T) string {
	t.Helper()
	dir := newProject(t, true, "", nil)
	if err := os.MkdirAll(filepath.Join(dir, "deploy/environments"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deploy/environments/staging.toml"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func expectLiveRelease(vm *remote.Fake) {
	vm.Expect("cat", "/opt/todo/current/release.json").Return(`{"release":"`+liveRelease+`","color":"blue","git_sha":"abcdef1234567890"}`, "", 0)
}

func TestRestoreNewClusterSafetyBackupRefusesBeforeStop(t *testing.T) {
	t.Parallel()
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			t.Parallel()
			vm := &remote.Fake{}
			expectRestoreLock(vm)
			vm.ExpectUpload(RemoteConfigPath("todo"))
			vm.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
			expectLiveRelease(vm)
			vm.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
			stdout, stderr := "", ""
			const diagnostic = "P00 ERROR: [051]: PostgreSQL version or system-id does not match stanza\n"
			if stream == "stdout" {
				stdout = diagnostic
			} else {
				stderr = diagnostic
			}
			vm.Expect(Command("todo", "--type=full", "--no-expire-auto", "backup")...).Return(stdout, stderr, 51)
			vm.Expect("sh", "-c").Return("", "", 0) // Release the deploy lock.
			err := Restore(context.Background(), RestoreOptions{Options: Options{Dir: restoreProject(t), Env: "staging", Remote: vm}, Into: "in-place", IKnow: true})
			if err == nil {
				t.Fatal("silently skipped the safety backup")
			}
			for _, want := range []string{"new cluster", "repository", "safety backup is impossible", "Try:", "--no-safety-backup", "ERROR: [051]"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error omitted %q: %v", want, err)
				}
			}
			vm.AssertDone(t)
			for _, c := range vm.Calls() {
				if slices.Contains(c.Argv, "stop") || slices.Contains(c.Argv, "restore") {
					t.Fatalf("failed safety backup changed services: %+v", c)
				}
			}
		})
	}
}

func TestRestoreTargetRecheckedAfterSafetyBackup(t *testing.T) {
	t.Parallel()
	dir := restoreProject(t)
	vm := &remote.Fake{}
	expectRestoreLock(vm)
	vm.ExpectUpload(RemoteConfigPath("todo"))
	vm.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
	expectLiveRelease(vm)
	vm.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
	vm.Expect(Command("todo", "--type=full", "--no-expire-auto", "backup")...).Return("", "", 0)
	vm.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
	vm.Expect(Command("todo", "--output=json", "info")...).Return("[]", "", 0)
	vm.Expect("sh", "-c").Return("", "", 0)
	var warnings strings.Builder
	err := Restore(context.Background(), RestoreOptions{Options: Options{Dir: dir, Env: "staging", Remote: vm, ErrOut: &warnings}, Into: "in-place", IKnow: true, Target: Target{Label: restoreLabel}})
	if err == nil || !strings.Contains(err.Error(), "no available backup") || !strings.Contains(warnings.String(), "Safety backup:") {
		t.Fatalf("missing target: %v %s", err, warnings.String())
	}
	vm.AssertDone(t)
	for _, c := range vm.Calls() {
		if slices.Contains(c.Argv, "stop") {
			t.Fatalf("expired target stopped services: %+v", c)
		}
	}
}

func TestRestoreFailureRecovery(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"stop app", "stop postgres", "restore", "migrate", "start", "smoke"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			vm := &remote.Fake{}
			expectRestoreLock(vm)
			vm.ExpectUpload(RemoteConfigPath("todo"))
			vm.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
			expectLiveRelease(vm)
			vm.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
			safe := stage == "stop app" || stage == "stop postgres"
			for i, color := range []string{deploy.Blue, deploy.Green} {
				for _, service := range []string{"api", "web"} {
					vm.Expect("docker", "ps", "-q", "--filter", "label=com.docker.compose.project=todo-"+color, "--filter", "label=com.docker.compose.service="+service).Return(color+"-"+service, "", 0)
					e := vm.Expect("docker", "stop", color+"-"+service)
					if stage == "stop app" {
						e.Return("", "stop app failed", 1)
						break
					}
					e.Return("", "", 0)
				}
				if stage == "stop app" && i == 0 {
					break
				}
			}
			if stage != "stop app" {
				vm.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
				e := vm.Expect(baseCompose("todo", "stop", "postgres")...)
				if stage == "stop postgres" {
					e.Return("", "stop postgres failed", 1)
					vm.Expect(baseCompose("todo", "start", "--wait", "postgres")...).Return("", "", 0) // Backend recovery.
				} else {
					e.Return("", "", 0)
					e = vm.Expect(baseCompose("todo", "run", "--rm", "--no-deps", "-T", "--entrypoint", "sh", "postgres")...)
					if stage == "restore" {
						e.Return("", "restore failed", 1)
					} else {
						e.Return("", "", 0)
						vm.Expect(baseCompose("todo", "start", "--wait", "postgres")...).Return("", "", 0)
						dir := "/opt/todo/releases/" + liveRelease
						e = vm.Expect(deploy.ComposeArgv("todo-migrate", dir, []string{dir + "/compose.yml"}, "--profile", "migrate", "run", "--rm", "-T", "migrate")...)
						if stage == "migrate" {
							e.Return("", "migrate failed", 1)
						} else {
							e.Return("", "", 0)
							e = vm.Expect(deploy.ComposeArgv("todo-blue", dir, []string{dir + "/compose.yml"}, "up", "-d", "--no-deps", "--wait", "api")...)
							if stage == "start" {
								e.Return("", "start failed", 1)
							} else {
								e.Return("", "", 0)
							}
						}
					}
				}
			}
			dir := "/opt/todo/releases/" + liveRelease
			if safe {
				if stage != "stop postgres" {
					vm.Expect(baseCompose("todo", "start", "--wait", "postgres")...).Return("", "", 0)
				}
				vm.Expect(deploy.ComposeArgv("todo-blue", dir, []string{dir + "/compose.yml"}, "up", "-d", "--no-deps", "--wait", "api")...).Return("", "", 0)
			} else if stage == "start" || stage == "smoke" {
				vm.Expect(deploy.ComposeArgv("todo-blue", dir, []string{dir + "/compose.yml"}, "stop", "api")...).Return("", "", 0)
			}
			vm.Expect("sh", "-c").Return("", "", 0)
			err := Restore(context.Background(), RestoreOptions{Options: Options{Dir: restoreProject(t), Env: "staging", Remote: vm}, Into: "in-place", IKnow: true, NoSafetyBackup: true,
				Sleep: func(context.Context, time.Duration) error { return nil }, HTTPGet: func(context.Context, string) (int, []byte, error) { return 503, nil, nil },
			})
			want := "api and web are STOPPED"
			if safe {
				want = "Restarted postgres and the live color"
			}
			if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("recovery: %v", err)
			}
			if !safe && (!strings.Contains(err.Error(), "'start' '--wait' 'postgres'") || !strings.Contains(err.Error(), "'todo-blue'")) {
				t.Fatalf("missing recovery commands: %v", err)
			}
			vm.AssertDone(t)
		})
	}
}

func TestPGBackRestSafetyBackupShell(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	script := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$TRACE"
case " $* " in *' --no-expire-auto '*) ;; *) exit 17 ;; esac
`
	if err := runexec.WriteExecutable(filepath.Join(dir, "pgbackrest"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	// Execute the exact sh -c payload used by Command against a real POSIX shell.
	argv := Command("todo", "--type=full", "--no-expire-auto", "backup")
	trace := filepath.Join(dir, "trace")
	_, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "sh", Args: argv[8:], Env: []string{"PATH=" + dir + ":" + os.Getenv("PATH"), "TRACE=" + trace}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(trace)
	if err != nil || string(data) != "--stanza=todo\n--type=full\n--no-expire-auto\nbackup\n" {
		t.Fatalf("pgbackrest argv: %q %v", data, err)
	}
}

func TestVerifyPreRestoreFailuresPreserveMetrics(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"missing", "time", "list SSH", "inspect SSH", "create"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			vm := &remote.Fake{}
			target := Target{Label: restoreLabel}
			vm.ExpectUpload(RemoteConfigPath("todo"))
			e := vm.Expect(Command("todo", "--output=json", "info")...)
			switch stage {
			case "missing":
				e.Return("[]", "", 0)
			case "time":
				target = Target{Time: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
				e.Return(fixture(t, "info.json"), "", 0)
			case "list SSH":
				e.Return("", "SSH lost", 1)
			default:
				e.Return(fixture(t, "info.json"), "", 0)
				vm.Expect("docker", "ps", "-aq").Return("postgres-id", "", 0)
				e = vm.Expect("docker", "inspect")
				if stage == "inspect SSH" {
					e.Return("", "SSH lost", 1)
				} else {
					e.Return(`{"Config":{"Image":"image","User":"1000:1000","Env":["PGDATA=/var/lib/postgresql/18/docker"]},"Mounts":[{"Destination":"/var/lib/postgresql"}]}`, "", 0)
					vm.Expect("docker", "create").Return("", "create failed", 1)
				}
			}
			var warnings strings.Builder
			_, err := Verify(context.Background(), Options{Dir: newProject(t, true, "", nil), Env: "staging", Remote: vm, ErrOut: &warnings}, target)
			if err == nil || strings.Contains(warnings.String(), "metrics could not be written") {
				t.Fatalf("pre-restore failure: %v %s", err, warnings.String())
			}
			vm.AssertDone(t)
			for _, call := range vm.Calls() {
				if call.Argv[0] == "sh" {
					t.Fatalf("pre-restore failure wrote metrics: %+v", call)
				}
			}
		})
	}
}

// cleanupRunner checks that cancellation of the restore does not cancel recovery.
type cleanupRunner struct {
	remote.Runner
	t *testing.T
}

// Run checks that recovery uses a context without cancellation.
func (r cleanupRunner) Run(ctx context.Context, c remote.Cmd) (runexec.Result, error) {
	r.t.Helper()
	if err := ctx.Err(); err != nil {
		r.t.Fatalf("recovery inherited cancellation: %v", err)
	}
	return r.Runner.Run(ctx, c)
}

func TestRestoreRecoveryReportsUnconfirmedServices(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"postgres", "live color", "stop"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			vm := &remote.Fake{}
			config := &project.Config{Modules: []string{"web"}}
			s := &Session{Env: Env{App: "todo", Config: config, Remote: cleanupRunner{Runner: vm, t: t}}}
			rel := deploy.Release{Release: liveRelease, Color: deploy.Green}
			state := &restoreRemote{postgres: "STOPPED", touched: stage == "stop"}
			if stage == "stop" {
				vm.Expect(liveCompose(s, rel, "stop")...).Return("", "SSH lost", 1)
			} else {
				e := vm.Expect(baseCompose("todo", "start", "--wait", "postgres")...)
				if stage == "postgres" {
					e.Return("", "SSH lost", 1)
				} else {
					e.Return("", "", 0)
					vm.Expect(liveCompose(s, rel, "up", "-d", "--no-deps", "--wait")...).Return("", "SSH lost", 1)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := restoreRecovery(ctx, s, rel, state, stage == "stop", errors.New("restore failed"))
			if !strings.Contains(err.Error(), "unconfirmed") || !strings.Contains(err.Error(), "Try:") || !strings.Contains(err.Error(), "'todo-green'") {
				t.Fatalf("recovery: %v", err)
			}
			vm.AssertDone(t)
		})
	}
}
