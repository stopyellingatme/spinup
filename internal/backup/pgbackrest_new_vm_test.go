package backup

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestPGBackRestNewVMConfiguration(t *testing.T) {
	t.Parallel()
	for _, op := range []string{"list", "status", "verify", "restore new", "restore in-place", "expire", "plan expire"} {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			vm := &remote.Fake{}
			vm.ExpectUpload(RemoteConfigPath("todo"))
			p := &PGBackRest{App: "todo", Remote: vm, Config: []byte("[global]\nrepo1-retention-full=4\n"), RetentionFull: 7}
			ctx := context.Background()
			var err error
			switch op {
			case "list", "status":
				vm.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
				if op == "list" {
					_, err = p.List(ctx)
				} else {
					_, err = p.Info(ctx)
				}
			case "verify", "restore new":
				expectRestoreContainer(t, vm, op == "verify", false)
				if op == "verify" {
					_, err = p.Verify(ctx, Target{Label: restoreLabel})
				} else {
					err = p.Restore(ctx, Target{Label: restoreLabel}, Into{New: true})
				}
			case "restore in-place":
				vm.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
				vm.Expect(baseCompose("todo", "stop", "postgres")...).Return("", "", 0)
				vm.Expect(baseCompose("todo", "run", "--rm", "--no-deps", "-T", "--entrypoint", "sh", "postgres")...).Return("", "", 0)
				vm.Expect(baseCompose("todo", "start", "--wait", "postgres")...).Return("", "", 0)
				err = p.Restore(ctx, Target{Label: restoreLabel}, Into{InPlace: true})
			case "expire", "plan expire":
				vm.Expect(Command("todo", p.expireArgs(op == "plan expire")...)...).Return("", "", 0)
				if op == "expire" {
					err = p.Expire(ctx)
				} else {
					err = p.PlanExpire(ctx)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			// A second operation on the same backend must reuse the uploaded config.
			vm.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
			if _, err := p.Info(ctx); err != nil {
				t.Fatal(err)
			}
			vm.AssertDone(t)
			uploads := vm.Uploads()
			if len(uploads) != 1 || uploads[0].Mode != 0o600 || string(uploads[0].Data) != "[global]\nrepo1-retention-full=7\n" {
				t.Fatalf("configuration uploads: %+v", uploads)
			}
			for _, c := range vm.Calls() {
				if slices.Contains(c.Argv, "stanza-create") {
					t.Fatal("a non-backup operation created the stanza")
				}
			}
		})
	}
}

func TestPGBackRestConfigUploadFailureRetries(t *testing.T) {
	t.Parallel()
	vm := &remote.Fake{}
	vm.ExpectUpload(RemoteConfigPath("todo")).Fail(errors.New("permission denied"))
	p := &PGBackRest{App: "todo", Remote: vm, Config: []byte("[global]\n")}
	if _, err := p.Info(context.Background()); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("upload failure: %v", err)
	}
	if len(vm.Calls()) != 0 {
		t.Fatal("ran pgBackRest after an upload failure")
	}
	vm.ExpectUpload(RemoteConfigPath("todo"))
	vm.Expect(Command("todo", "--output=json", "info")...).Return("[]", "", 0)
	if _, err := p.Info(context.Background()); err != nil {
		t.Fatal(err)
	}
	vm.AssertDone(t)
}

// redactingBackupRunner supplies the secret values to the runner's redaction contract.
type redactingBackupRunner struct{ remote.Runner }

func (r redactingBackupRunner) Run(ctx context.Context, c remote.Cmd) (runexec.Result, error) {
	c.Redact = append(c.Redact, "test-secret")
	return r.Runner.Run(ctx, c)
}

func TestPGBackRestFailureDiagnostics(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"backend", "restore"} {
		for _, stream := range []string{"stdout", "stderr", "both"} {
			t.Run(command+"/"+stream, func(t *testing.T) {
				t.Parallel()
				vm := &remote.Fake{}
				stdout, stderr := "", ""
				const diagnostic = "P00 WARN: retry test-secret\nP00 ERROR: [055]: unable to open missing file test-secret\n"
				if stream != "stderr" {
					stdout = "P00 INFO: ordinary output\n" + diagnostic
				}
				if stream != "stdout" {
					// More than the exec error's stderr tail must not hide the diagnostic.
					stderr = diagnostic + strings.Repeat("P00 INFO: ordinary output\n", 25)
				}
				var progress strings.Builder
				p := &PGBackRest{App: "todo", Remote: redactingBackupRunner{vm}, Progress: &progress}
				var err error
				if command == "backend" {
					vm.Expect(Command("todo", "--output=json", "info")...).Return(stdout, stderr, 55)
					_, err = p.Info(context.Background())
				} else {
					vm.Expect("docker", "exec", "restore").Return(stdout, stderr, 55)
					_, err = p.remoteCommand(context.Background(), "docker", "exec", "restore")
				}
				if err == nil {
					t.Fatal("lost command failure")
				}
				for _, want := range []string{"WARN: retry ***", "ERROR: [055]: unable to open missing file ***", "Try:"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error omitted %q: %v", want, err)
					}
				}
				if strings.Contains(err.Error()+progress.String(), "test-secret") {
					t.Fatal("diagnostics exposed a secret")
				}
				var exit *runexec.ExitError
				if !errors.As(err, &exit) || exit.Code != 55 {
					t.Fatalf("lost wrapped exit error: %v", err)
				}
				vm.AssertDone(t)
			})
		}
	}
}
