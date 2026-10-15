package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/backup"
	_ "github.com/stopyellingatme/spinup/internal/backup/pgdump"
	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/remote"
)

const dumpLabel = "20261015T120000ZF"
const releaseID = "20261015T082351Z-abcdef123456"

func dumpProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range map[string]string{
		"spinup.toml": `modules = ["deploy", "backups"]
[app]
name = "todo"
module = "example.com/todo"
[backups]
backend = "pgdump"
[env.staging]
host = "203.0.113.10"
user = "deploy"
`,
		"deploy/environments/staging.toml": "",
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func dumpListing(vm *remote.Fake) {
	vm.Expect("sh", "-eu", "-c").Return("exists\n/opt/todo/backups/pgdump/"+dumpLabel+".dump\t20\t1792065600\n", "", 0)
}

func TestVerifyPgdumpOnlyRecordsAttemptedRestore(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"time", "typo", "missing", "ssh", "ready", "restore"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			vm := &remote.Fake{}
			target := backup.Target{Label: dumpLabel}
			switch stage {
			case "time":
				target = backup.Target{Time: time.Now()}
			case "typo":
				target.Label = "typo"
			case "missing":
				vm.Expect("sh", "-eu", "-c").Return("exists\n", "", 0)
			default:
				dumpListing(vm)
				e := vm.Expect("docker", "compose")
				if stage == "ssh" {
					e.Return("", "SSH lost", 1)
				} else {
					e.Return("app\n", "", 0)
					vm.Expect("docker", "compose").Return("app\n", "", 0)
					vm.Expect("docker", "compose").Return("postgres-id\n", "", 0)
					vm.Expect("docker", "inspect").Return("sha256:image\n", "", 0)
					vm.Expect("docker", "create").Return("id", "", 0)
					vm.Expect("docker", "start").Return("", "", 0)
					e = vm.Expect("sh", "-eu", "-c")
					if stage == "ready" {
						e.Return("", "not ready", 1)
					} else {
						e.Return("", "", 0)
						vm.Expect("sh", "-eu", "-c").Return("", "restore failed", 1)
					}
					vm.Expect("sh", "-eu", "-c").Return("", "", 0) // Cleanup.
				}
			}
			if stage == "restore" {
				vm.Expect("sh", "-ec").Return("backup_last_verify_timestamp_seconds 123\n", "", 0)
				vm.Expect("mkdir", "-p", "/opt/todo/backups/metrics").Return("", "", 0)
				vm.Expect("chmod", "0755", "/opt/todo/backups/metrics").Return("", "", 0)
				vm.ExpectUpload(backup.MetricsPath("todo"))
			}
			_, err := backup.Verify(context.Background(), backup.Options{Dir: dumpProject(t), Env: "staging", Remote: vm}, target)
			if err == nil {
				t.Fatal("expected failure")
			}
			vm.AssertDone(t)
			want := 0
			if stage == "restore" {
				want = 1
			}
			if len(vm.Uploads()) != want {
				t.Fatalf("metrics uploads: %+v", vm.Uploads())
			}
			if want == 1 && !strings.Contains(string(vm.Uploads()[0].Data), "backup_last_verify_ok 0\n") {
				t.Fatal("missing failed verification metric")
			}
		})
	}
}

func TestPgdumpRestorePlan(t *testing.T) {
	t.Parallel()
	vm := &remote.Fake{}
	var out strings.Builder
	err := backup.Restore(context.Background(), backup.RestoreOptions{Options: backup.Options{Dir: dumpProject(t), Env: "staging", Remote: vm, Out: &out}, Into: "in-place", Plan: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"staging database while postgres runs", "Rename the databases and retain the old database", "dropdb command"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "Stop postgres") || strings.Contains(out.String(), "Start postgres") {
		t.Fatalf("physical restore plan: %s", out.String())
	}
	if len(vm.Calls()) != 0 {
		t.Fatal("plan called the VM")
	}
}

func TestPgdumpRestoreFailureRecovery(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"staging", "rename"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			vm := &remote.Fake{}
			vm.Expect("mkdir", "/opt/todo/deploy.lock").Return("", "", 0)
			vm.ExpectUpload("/opt/todo/deploy.lock/owner.json")
			dumpListing(vm)
			vm.Expect("cat", "/opt/todo/current/release.json").Return(`{"release":"`+releaseID+`","color":"blue","git_sha":"abcdef1234567890"}`, "", 0)
			dumpListing(vm)
			for _, color := range []string{"blue", "green"} {
				for _, service := range []string{"api", "web"} {
					vm.Expect("docker", "ps", "-q", "--filter", "label=com.docker.compose.project=todo-"+color, "--filter", "label=com.docker.compose.service="+service).Return(color+"-"+service, "", 0)
					vm.Expect("docker", "stop", color+"-"+service).Return("", "", 0)
				}
			}
			dumpListing(vm)
			vm.Expect("docker", "compose").Return("app\n", "", 0)
			vm.Expect("docker", "compose").Return("app\n", "", 0)
			vm.Expect("docker", "compose").Return("f\n", "", 0) // Old name is free.
			vm.Expect("docker", "compose").Return("", "", 0)    // Create staging.
			e := vm.Expect("sh", "-eu", "-c")
			if stage == "staging" {
				e.Return("", "staging failed", 1)
			} else {
				e.Return("", "", 0)
				vm.Expect("docker", "compose").Return("", "rename failed", 1)
			}
			if stage == "staging" {
				dir := "/opt/todo/current"
				vm.Expect(deploy.ComposeArgv("todo", dir, []string{dir + "/compose.edge.yml", dir + "/compose.backups.yml"}, "start", "--wait", "postgres")...).Return("", "", 0)
				dir = "/opt/todo/releases/" + releaseID
				vm.Expect(deploy.ComposeArgv("todo-blue", dir, []string{dir + "/compose.yml"}, "up", "-d", "--no-deps", "--wait", "api")...).Return("", "", 0)
			}
			vm.Expect("sh", "-c").Return("", "", 0) // Unlock.
			err := backup.Restore(context.Background(), backup.RestoreOptions{Options: backup.Options{Dir: dumpProject(t), Env: "staging", Remote: vm}, Into: "in-place", IKnow: true, NoSafetyBackup: true, Target: backup.Target{Label: dumpLabel}})
			want := "api and web are STOPPED"
			if stage == "staging" {
				want = "Restarted postgres and the live color"
			}
			if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("recovery: %v", err)
			}
			vm.AssertDone(t)
		})
	}
}
