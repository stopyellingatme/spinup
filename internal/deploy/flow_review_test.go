package deploy

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestUnlockProtected(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		confirm project.Confirm
		allowed bool
	}{
		{"noninteractive", project.Confirm{}, false},
		{"yes outside CI", project.Confirm{Yes: true}, false},
		{"CI without yes", project.Confirm{CI: true}, false},
		{"wrong name", project.Confirm{Interactive: true, In: strings.NewReader("other\n")}, false},
		{"typed name", project.Confirm{Interactive: true, In: strings.NewReader("staging\n")}, true},
		{"CI with yes", project.Confirm{CI: true, Yes: true}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			p := filepath.Join(x.root, "spinup.toml")
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, append(data, []byte("protected = true\n")...), 0644); err != nil {
				t.Fatal(err)
			}
			x.o.Confirm = tt.confirm
			x.o.Confirm.Out = &x.out
			x.vm.Expect("sh", "-c").Return("", "", 3)
			x.ok("rm", "-rf", "--", testRoot+"/deploy.lock", testRoot+"/deploy.lock.guard")
			x.vm.ExpectClose().Return("", "", 0)
			err = Unlock(context.Background(), x.o, x.d, true)
			if tt.allowed {
				if err != nil {
					t.Fatal(err)
				}
				x.vm.AssertDone(t)
			} else if err == nil || x.dialed != 0 {
				t.Fatalf("protected unlock: error=%v, dials=%d", err, x.dialed)
			}
		})
	}
}

type heartbeatVM struct {
	remote.Runner
	refreshed atomic.Int32
	lost      bool
}

func (v *heartbeatVM) Upload(ctx context.Context, data []byte, path string, mode fs.FileMode) error {
	if strings.Contains(path, "/deploy.lock.refresh-") {
		return nil
	}
	return v.Runner.Upload(ctx, data, path, mode)
}
func (v *heartbeatVM) Run(ctx context.Context, c remote.Cmd) (runexec.Result, error) {
	if len(c.Argv) == 3 && strings.Contains(c.Argv[2], "/deploy.lock.refresh-") {
		v.refreshed.Add(1)
		if v.lost {
			return runexec.Result{ExitCode: 4}, &runexec.ExitError{Code: 4, StderrTail: "owner changed"}
		}
		return runexec.Result{}, nil
	}
	return v.Runner.Run(ctx, c)
}
func TestFlowHeartbeatDuringLongStep(t *testing.T) {
	t.Parallel()
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "refresh", true: "owner changed"}[lost], func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			vm := &heartbeatVM{Runner: x.vm, lost: lost}
			x.d.Dial = func(context.Context, project.Env, bool) (remote.Runner, error) { return vm, nil }
			x.o.LockTTL = 15 * time.Millisecond
			x.expectLocalGit()
			x.expectConnect()
			x.expectLock()
			x.expectBegin("")
			x.journal(1)
			x.expectReadCurrent("")
			if !lost {
				x.journal(1)
			}
			x.expectUnlockAndClose()
			stepErr := errors.New("long step finished")
			x.d.PreDeploy = func() []Hook {
				return []Hook{{Name: "slow", Run: func(ctx context.Context, _ *HookContext) error {
					timer := time.NewTimer(100 * time.Millisecond)
					defer timer.Stop()
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-timer.C:
						return stepErr
					}
				}}}
			}
			err := x.run()
			if vm.refreshed.Load() < 1 {
				t.Fatalf("no heartbeat during long step: %v", err)
			}
			if lost && (err == nil || !strings.Contains(err.Error(), "refresh deploy lock")) {
				t.Fatalf("lost owner did not stop deploy: %v", err)
			}
			x.vm.AssertDone(t)
		})
	}
}

func TestFlowRejectsExistingRelease(t *testing.T) {
	t.Parallel()
	for _, history := range []bool{false, true} {
		t.Run(map[bool]string{false: "directory", true: "history"}[history], func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			x.o.fill()
			x.d.fill()
			vm := localVM{root: t.TempDir()}
			writeTree(t, vm.root, map[string]string{"releases/" + testRelease + "/.env": "SPINUP_COLOR=blue\n"})
			if history {
				writeTree(t, vm.root, map[string]string{"releases.json": `[{"release":"` + testRelease + `"}]`})
				if err := os.RemoveAll(filepath.Join(vm.root, "releases")); err != nil {
					t.Fatal(err)
				}
			}
			f := &flow{o: x.o, d: x.d, l: &local{app: "app", sha: testSHA}, vm: vm, release: testRelease, out: &x.out, errOut: &x.errOut}
			err := f.begin(context.Background())
			if err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("existing release accepted: %v", err)
			}
			if _, err := os.Stat(filepath.Join(vm.root, "deploy.state")); !os.IsNotExist(err) {
				t.Fatal("duplicate changed journal")
			}
		})
	}
}

func TestShellEdgeStagePreservesLiveConfiguration(t *testing.T) {
	t.Parallel()
	vm := localVM{root: t.TempDir()}
	writeTree(t, vm.root, map[string]string{
		"caddy/Caddyfile": "old\n", "caddy/Caddyfile.d/live.caddy": "valid\n",
		"postgres/conf.d/live.conf":              "old\n",
		"releases/" + testRelease + "/Caddyfile": "new\n", "releases/" + testRelease + "/Caddyfile.d/bad.caddy": "invalid\n",
		"releases/" + testRelease + "/postgres/conf.d/new.conf": "new\n",
	})
	run(t, vm, "sh", "-c", edgeSyncScript, "spinup-edge", "/opt/app", "/opt/app/releases/"+testRelease)
	for path, want := range map[string]string{"caddy/Caddyfile": "old\n", "caddy/Caddyfile.d/live.caddy": "valid\n", "postgres/conf.d/live.conf": "old\n"} {
		data, err := os.ReadFile(filepath.Join(vm.root, path))
		if err != nil || string(data) != want {
			t.Errorf("live %s changed before validation: %q, %v", path, data, err)
		}
	}
}

// releaseUploadVM isolates the secrets-directory preparation from this release test.
// The VM runs Linux. BSD chmod does not accept the Linux script's argument order.
type releaseUploadVM struct{ localVM }

func (v releaseUploadVM) Run(ctx context.Context, c remote.Cmd) (runexec.Result, error) {
	if len(c.Argv) > 4 && c.Argv[3] == "spinup-secrets" {
		return runexec.Result{}, os.MkdirAll(v.path(c.Argv[4]), 0700)
	}
	return v.localVM.Run(ctx, c)
}

func TestFlowRedeployPreservesLiveRelease(t *testing.T) {
	t.Parallel()
	for _, source := range []string{testRelease, testRelease + "-r1"} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			x.o.fill()
			x.d.fill()
			x.o.Redeploy = true
			vm := localVM{root: t.TempDir()}
			oldEnv := "SPINUP_COLOR=blue\n"
			writeTree(t, vm.root, map[string]string{
				"releases/" + testRelease + "/.env":         oldEnv,
				"releases/" + testRelease + "/release.json": `{"release":"` + testRelease + `","color":"blue"}`,
				"releases/" + testRelease + "-r2/.partial":  "reserved",
				"releases.json": `[{"release":"` + testRelease + `-r1"},{"release":"` + testRelease + `"}]`,
				"deploy.state":  "previous journal\n",
			})
			writeTree(t, vm.root, map[string]string{"caddy/upstream.caddy": string(UpstreamFile(Blue))})
			run(t, vm, "ln", "-sfn", "releases/"+testRelease, "/opt/app/current")
			images := []Image{{Service: "api", Tag: "app-api:" + testRelease, Ref: "app-api:" + testRelease}}
			f := &flow{o: x.o, d: x.d, l: &local{app: "app", sha: testSHA, env: project.Env{Name: "staging"}}, vm: releaseUploadVM{vm}, release: source, images: images, out: &x.out, errOut: &x.errOut}
			// Select under the deploy lock, before the journal or upload changes.
			if err := f.selectRelease(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := testRelease + "-r3"
			if f.release != want || !ValidReleaseID(want) || !sameSHA(want, testSHA) {
				t.Fatalf("redeploy ID=%s", f.release)
			}
			if f.images[0].Tag != "app-api:"+testRelease || f.images[0].Ref != "app-api:"+testRelease {
				t.Fatal("redeploy changed image references")
			}
			if err := os.WriteFile(filepath.Join(vm.root, "releases", want), []byte("collision"), 0644); err != nil {
				t.Fatal(err)
			}
			// Files and dangling symlinks also reserve release IDs.
			if err := os.Symlink("missing", filepath.Join(vm.root, "releases", testRelease+"-r4")); err != nil {
				t.Fatal(err)
			}
			if err := f.selectRelease(context.Background()); err != nil {
				t.Fatal(err)
			}
			if f.release != testRelease+"-r5" {
				t.Fatalf("reserved names reused: %s", f.release)
			}
			if _, err := f.upload(context.Background()); err != nil {
				t.Fatal(err)
			}
			newEnv, err := os.ReadFile(filepath.Join(vm.root, "releases", f.release, ".env"))
			if err != nil || !strings.Contains(string(newEnv), "SPINUP_COLOR=green") {
				t.Fatalf("new color environment = %q, %v", newEnv, err)
			}
			snapshot, err := readRelease(context.Background(), vm, "app", f.release)
			if err != nil || snapshot.Release != f.release || snapshot.Color != Green || snapshot.Images[0].Tag != "app-api:"+testRelease {
				t.Fatalf("redeploy snapshot = %#v, %v", snapshot, err)
			}
			data, err := os.ReadFile(filepath.Join(vm.root, "current", ".env"))
			if err != nil || string(data) != oldEnv {
				t.Fatalf("live .env changed: %q, %v", data, err)
			}
			history, err := ReadReleases(context.Background(), vm, "app")
			if err != nil || len(history) != 2 || history[1].Release != testRelease {
				t.Fatalf("history changed: %v, %v", history, err)
			}
			if _, err := f.record(context.Background()); err != nil {
				t.Fatal(err)
			}
			history, err = ReadReleases(context.Background(), vm, "app")
			if err != nil || len(history) != 3 || history[0].Release != f.release || history[2].Release != testRelease {
				t.Fatalf("redeploy lost rollback target: %v, %v", history, err)
			}
			data, err = os.ReadFile(filepath.Join(vm.root, "releases", testRelease, ".env"))
			if err != nil || string(data) != oldEnv {
				t.Fatalf("record overwrote previous release: %q, %v", data, err)
			}

		})
	}
}

type edgeValidationVM struct {
	localVM
	invalid bool
	stopped bool
	started bool
}

func (v *edgeValidationVM) Run(ctx context.Context, c remote.Cmd) (runexec.Result, error) {
	if len(c.Argv) > 1 && c.Argv[0] == "docker" {
		if strings.Contains(strings.Join(c.Argv, " "), " validate ") {
			if !strings.Contains(strings.Join(c.Argv, " "), "/etc/caddy/.next/Caddyfile") {
				return runexec.Result{}, errors.New("validation did not use the staged tree")
			}
			if v.invalid {
				return runexec.Result{ExitCode: 1}, &runexec.ExitError{Code: 1, StderrTail: "invalid snippet"}
			}
			return runexec.Result{}, nil
		}
		switch c.Argv[1] {
		case "ps":
			if v.stopped {
				return runexec.Result{}, nil
			}
			return runexec.Result{Stdout: []byte("caddy1\n")}, nil
		case "compose":
			v.started = true
			return runexec.Result{}, nil
		}
	}
	return v.localVM.Run(ctx, c)
}
func TestFlowEdgeValidationFailurePreservesLiveTree(t *testing.T) {
	t.Parallel()
	for _, stopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "stopped"}[stopped], func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			x.o.fill()
			vm := &edgeValidationVM{localVM: localVM{root: t.TempDir()}, invalid: true, stopped: stopped}
			writeTree(t, vm.root, map[string]string{
				"caddy/Caddyfile": "old\n", "caddy/Caddyfile.d/live.caddy": "valid\n",
				"caddy/upstream.caddy":                               string(UpstreamFile(Blue)),
				"postgres/conf.d/live.conf":                          "old\n",
				"releases/" + testRelease + "/Caddyfile":             "import Caddyfile.d/*.caddy\n",
				"releases/" + testRelease + "/Caddyfile.d/new.caddy": "new\n",
			})
			f := &flow{o: x.o, l: &local{app: "app"}, vm: vm, release: testRelease, out: &x.out}
			if _, err := f.edge(context.Background()); err == nil {
				t.Fatal("invalid Caddy configuration accepted")
			}
			for path, want := range map[string]string{"caddy/Caddyfile": "old\n", "caddy/Caddyfile.d/live.caddy": "valid\n", "postgres/conf.d/live.conf": "old\n"} {
				data, err := os.ReadFile(filepath.Join(vm.root, path))
				if err != nil || string(data) != want {
					t.Errorf("failed validation changed %s: %q, %v", path, data, err)
				}
			}
			if vm.started {
				t.Fatal("started edge after validation failure")
			}
			vm.invalid = false
			if _, err := f.edge(context.Background()); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(filepath.Join(vm.root, "caddy", "Caddyfile.d", "new.caddy")); err != nil || string(data) != "new\n" {
				t.Fatalf("validated snippets not installed: %q, %v", data, err)
			}
			if _, err := os.Stat(filepath.Join(vm.root, "caddy", "Caddyfile.d", "live.caddy")); !os.IsNotExist(err) {
				t.Fatal("stale snippet remains after promotion")
			}
			if !vm.started {
				t.Fatal("validated edge not started")
			}

		})
	}
}
