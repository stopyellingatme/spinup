package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/migrate"
)

type recoveryExec struct{ goCalls []string }

func (r *recoveryExec) Run(ctx context.Context, cmd exec.Cmd) (exec.Result, error) {
	if cmd.Name == "go" {
		r.goCalls = append(r.goCalls, strings.Join(cmd.Args, " "))
		return exec.Result{}, nil
	}
	return (exec.OSRunner{}).Run(ctx, cmd)
}

func TestContinueResolvedConflictsRunsChecks(t *testing.T) {
	// The phase and extension registries require serial tests.
	oldPhases, oldExtensions := CurrentPhases(), Extensions()
	defer SetPhases(oldPhases)
	defer func() { hooksMu.Lock(); extensions = oldExtensions; hooksMu.Unlock() }()
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "plan", true: "extension"}[after], func(t *testing.T) {
			hooksMu.Lock()
			extensions = nil
			hooksMu.Unlock()
			r, root := runFixture(t, !after)
			writePlanFile(t, root, "api/go.mod", "module example.com/demo/api\n", 0644)
			testGit(t, root, "add", ".")
			testGit(t, root, "commit", "-m", "Add API")
			commands := &recoveryExec{}
			r.Exec = commands
			doctorCalls := 0
			registry := &migrate.Registry{}
			p := newPhaseHooks(registry.Pending, func(context.Context, doctor.Scope, *doctor.Context) []doctor.Report {
				doctorCalls++
				return nil
			})
			SetPhases(Phases{GoFix: p.GoFix, Verify: p.Verify})
			if after {
				RegisterExtension(Extension{Name: "conflicting-ui", Run: func(_ context.Context, s *Step) error {
					writePlanFile(t, s.Worktree, "file.txt", "<<<<<<< ours\nours\n=======\ntheirs\n>>>>>>> theirs\n", 0644)
					s.Plan.Files[0].Action = ActionConflict
					s.Conflicts = []string{"file.txt"}
					return nil
				}})
			}
			ctx := context.Background()
			if err := r.Run(ctx, Options{To: "0.2.0", Yes: true}); err == nil || !strings.Contains(err.Error(), "conflicts") {
				t.Fatalf("expected conflict, got %v", err)
			}
			work := filepath.Join(root, ".spinup/upgrade/0.2.0")
			writePlanFile(t, work, "file.txt", "resolved\n", 0644)
			// A new Runner reads the saved state, as a new CLI process does.
			resumed := &Runner{Root: root, Source: r.Source, Exec: commands, Out: r.Out}
			if err := resumed.Continue(ctx, Options{Yes: true}); err != nil {
				t.Fatal(err)
			}
			want := []string{"fix ./...", "build ./...", "vet ./...", "test ./..."}
			if !reflect.DeepEqual(commands.goCalls, want) || doctorCalls != 1 {
				t.Fatalf("go commands=%v doctor calls=%d", commands.goCalls, doctorCalls)
			}
		})
	}
}

func TestUIChoiceSurvivesInterruptedStep(t *testing.T) {
	oldPhases := CurrentPhases()
	defer SetPhases(oldPhases)
	defer uiEnabled.Store(false)
	for _, phase := range []string{"conflicts", "Deps"} {
		t.Run(phase, func(t *testing.T) {
			r, root := runFixture(t, phase == "conflicts")
			source := r.Source.(chainSource)
			for _, v := range source {
				web := uiVersion(v.Version, map[string]string{"0.1.0": "1.7.0", "0.2.0": "1.8.0"}[v.Version], v.Version+"\n")
				for path, file := range web.Packs.(fstest.MapFS) {
					v.Packs.(fstest.MapFS)[path] = file
				}
			}
			lock, err := r.lock()
			if err != nil {
				t.Fatal(err)
			}
			lock.Packs = append(lock.Packs, "web")
			lock.Answers["shadcn_registry"] = "1.7.0"
			if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
				t.Fatal(err)
			}
			writePlanFile(t, root, uiButton, "0.1.0\n", 0644)
			testGit(t, root, "add", ".")
			testGit(t, root, "commit", "-m", "Add web")
			p := Phases{}
			if phase == "Deps" {
				p.Deps = func(context.Context, *Step) error { return errors.New("interrupted dependencies") }
			}
			SetPhases(p)
			uiEnabled.Store(true)
			if err := r.Run(context.Background(), Options{To: "0.2.0", Yes: true, NoVerify: true}); err == nil {
				t.Fatal("expected interruption")
			}
			uiEnabled.Store(false)
			SetPhases(Phases{})
			if phase == "conflicts" {
				writePlanFile(t, filepath.Join(root, ".spinup/upgrade/0.2.0"), "file.txt", "resolved\n", 0644)
			}
			resumed := &Runner{Root: root, Source: r.Source, Out: r.Out}
			if err := resumed.Continue(context.Background(), Options{Yes: true}); err != nil {
				t.Fatal(err)
			}
			lock, err = manifest.Load(filepath.Join(root, ".spinup/upgrade/0.2.0/spinup.lock"))
			if err != nil {
				t.Fatal(err)
			}
			if lock.Answers["shadcn_registry"] != "1.8.0" {
				t.Fatalf("UI choice lost: %v", lock.Answers)
			}
			data, err := os.ReadFile(filepath.Join(root, ".spinup/upgrade/0.2.0", uiButton))
			if err != nil || string(data) != "0.2.0\n" {
				t.Fatalf("UI content=%q error=%v", data, err)
			}
		})
	}
}
