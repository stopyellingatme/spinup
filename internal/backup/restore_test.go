package backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/ui"
)

const restoreLabel = "20261015-082351F"
const liveRelease = "20261015T082351Z-abcdef123456"

func TestRestoreGuardsAndPlan(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                       string
		know, protected, plan, now bool
		window, want               string
	}{
		{name: "needs i-know", want: "--i-know"},
		{name: "protected", know: true, protected: true, want: "protected environment"},
		{name: "outside window", know: true, window: "Sun 02:00-05:00", want: "outside maintenance window"},
		{name: "bad window", know: true, now: true, window: "bad", want: "invalid maintenance window"},
		{name: "plan", protected: true, plan: true, window: "Sun 02:00-05:00", want: "Plan: Take a safety backup"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := newProject(t, true, "", nil)
			path := filepath.Join(dir, "spinup.toml")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if tt.protected {
				data = append(data, []byte("protected = true\n")...)
			}
			if tt.window != "" {
				data = append(data, []byte("\n[maintenance]\nwindow = \""+tt.window+"\"\n")...)
			}
			if err = os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			vm := &remote.Fake{}
			var out strings.Builder
			o := RestoreOptions{Options: Options{Dir: dir, Env: "staging", Remote: vm, Out: &out}, Into: "in-place", IKnow: tt.know, Plan: tt.plan, Now: tt.now, Clock: func() time.Time { return time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC) }}
			err = Restore(context.Background(), o)
			if tt.plan {
				if err != nil || !strings.Contains(out.String(), tt.want) {
					t.Fatalf("plan: %s %v", out.String(), err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error: %v", err)
			}
			if len(vm.Calls()) != 0 || len(vm.Uploads()) != 0 {
				t.Fatal("refusal or plan mutated the VM")
			}
		})
	}
}

func TestRestoreLockContention(t *testing.T) {
	t.Parallel()
	vm := &remote.Fake{}
	vm.Expect("mkdir", "/opt/todo/deploy.lock").Return("", "held", 1)
	info := deploy.LockInfo{Owner: "operator", Acquired: time.Now().UTC(), TTLSeconds: 3600, Release: "busy"}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	vm.Expect("sh", "-c").Return(string(data), "", 0)
	err = Restore(context.Background(), RestoreOptions{Options: Options{Dir: newProject(t, true, "", nil), Env: "staging", Remote: vm}, Into: "in-place", IKnow: true})
	if ui.ExitCode(err) != ui.ExitLocked || !strings.Contains(err.Error(), "deploy lock is held") {
		t.Fatalf("error: %v", err)
	}
	vm.AssertDone(t)
}

func expectRestoreLock(vm *remote.Fake) {
	vm.Expect("mkdir", "/opt/todo/deploy.lock").Return("", "", 0)
	vm.ExpectUpload("/opt/todo/deploy.lock/owner.json")
}

func TestRestoreInPlace(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ fail, extras bool }{{}, {fail: true}, {extras: true}, {fail: true, extras: true}} {
		fail := tc.fail
		name := "success"
		if fail {
			name = "failure prints recovery command"
		}
		if tc.extras {
			name += " with worker and sidecar"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := newProject(t, true, "", nil)
			if err := os.MkdirAll(filepath.Join(dir, "deploy/environments"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "deploy/environments/staging.toml"), []byte("smoke_urls = [\"/ready\"]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			files := []string{"/opt/todo/releases/" + liveRelease + "/compose.yml"}
			if tc.extras {
				// The deployed release has a sidecar, and the local spinup.toml has neither
				// the jobs module nor the service. The stop and the start act on the whole
				// color projects, so the release decides what runs.
				files = append(files, "/opt/todo/releases/"+liveRelease+"/compose.services.yml")
			}
			vm := &remote.Fake{}
			expectRestoreLock(vm)
			vm.ExpectUpload(RemoteConfigPath("todo"))
			// The latest target is selected before the safety backup exists.
			vm.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
			rel := deploy.Release{Release: liveRelease, Color: deploy.Blue, GitSHA: "abcdef1234567890"}
			if tc.extras {
				rel.Services = []deploy.ReleaseService{{Name: "ocr", Port: 8080, Health: "/healthz"}}
			}
			data, err := json.Marshal(rel)
			if err != nil {
				t.Fatal(err)
			}
			vm.Expect("cat", "/opt/todo/current/release.json").Return(string(data), "", 0)
			vm.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
			vm.Expect(Command("todo", "--type=full", "--no-expire-auto", "backup")...).Return("", "", 0)
			vm.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
			vm.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0) // Re-check before stopping.
			for _, color := range []string{deploy.Blue, deploy.Green} {
				vm.Expect("docker", "ps", "-q", "--filter", "label=com.docker.compose.project=todo-"+color).Return(color+"-api\n"+color+"-worker\n"+color+"-ocr\n", "", 0)
				vm.Expect("docker", "stop", color+"-api", color+"-worker", color+"-ocr").Return("", "", 0)
			}
			vm.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
			vm.Expect(baseCompose("todo", "stop", "postgres")...).Return("", "", 0)
			restore := vm.Expect(baseCompose("todo", "run", "--rm", "--no-deps", "-T", "--entrypoint", "sh", "postgres")...)
			if fail {
				restore.Return("", "restore failed", 1)
			} else {
				restore.Return("", "", 0)
				vm.Expect(baseCompose("todo", "start", "--wait", "postgres")...).Return("", "", 0)
				dir := "/opt/todo/releases/" + liveRelease
				vm.Expect(deploy.ComposeArgv("todo-migrate", dir, files, "--profile", "migrate", "run", "--rm", "-T", "migrate")...).Return("", "", 0)
				vm.Expect(deploy.ComposeArgv("todo-blue", dir, files, "up", "-d", "--no-deps", "--wait")...).Return("", "", 0)
			}
			vm.Expect("sh", "-c").Return("", "", 0) // Release the lock, also after failure.
			var out, errOut strings.Builder
			var paths []string
			err = Restore(context.Background(), RestoreOptions{Options: Options{Dir: dir, Env: "staging", Remote: vm, Out: &out, ErrOut: &errOut}, Into: "in-place", IKnow: true, Confirm: project.Confirm{}, HTTPGet: func(_ context.Context, url string) (int, []byte, error) {
				paths = append(paths, url)
				return 200, []byte(`{"version":"abcdef123456"}`), nil
			}})
			if fail {
				want := "spinup backup restore --env staging --into in-place --label 20261015-082351F_20261015-082353D --i-know --no-safety-backup --now"
				if err == nil || !strings.Contains(errOut.String(), want) {
					t.Fatalf("recovery: %s %v", errOut.String(), err)
				}
			} else if err != nil || len(paths) != 3 {
				t.Fatalf("restore: %v, smoke: %v", err, paths)
			}
			vm.AssertDone(t)
			if !fail {
				dir := "/opt/todo/releases/" + liveRelease
				for _, want := range [][]string{
					deploy.ComposeArgv("todo-blue", dir, files, "up", "-d", "--no-deps", "--wait"),
					deploy.ComposeArgv("todo-migrate", dir, files, "--profile", "migrate", "run", "--rm", "-T", "migrate"),
				} {
					found := false
					for _, call := range vm.Calls() {
						if slices.Equal(call.Argv, want) {
							found = true
						}
					}
					if !found {
						t.Fatalf("missing exact argv: %v", want)
					}
				}
			}
			joined := ""
			for _, call := range vm.Calls() {
				joined += strings.Join(call.Argv, " ") + "\n"
			}
			if !strings.Contains(joined, "--delta restore") {
				t.Fatal("in-place restore omitted --delta")
			}
		})
	}
}

func TestRestoreNewNeedsNoConfirmation(t *testing.T) {
	t.Parallel()
	vm := &remote.Fake{}
	vm.ExpectUpload(RemoteConfigPath("todo"))
	expectRestoreContainer(t, vm, false, false)
	var out strings.Builder
	err := Restore(context.Background(), RestoreOptions{Options: Options{Dir: newProject(t, true, "", nil), Env: "staging", Remote: vm, Out: &out}, Target: Target{Label: restoreLabel}, Into: "new"})
	if err != nil || !strings.Contains(out.String(), "spinup server ssh --env staging -L 5433:127.0.0.1:5433") {
		t.Fatalf("new: %s %v", out.String(), err)
	}
	vm.AssertDone(t)
}
