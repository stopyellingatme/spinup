package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/merge"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func TestRunPlanChain(t *testing.T) {
	t.Parallel()
	r, root := runFixture(t, false)
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "unrelated-link")); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "unrelated-link")
	testGit(t, root, "commit", "-m", "unrelated link")
	source := r.Source.(chainSource)
	source["0.3.0"] = planVersion("0.3.0", planSpec{path: "file.txt", data: "third\n"})
	source["0.2.0"].KitMigration = []byte(`{"schema":1,"version":"0.2.0","steps":[{"id":"kit-note","desc":"Review this change","kind":"manual"}]}`)
	report, err := r.Plan(context.Background(), Options{To: "0.3.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Steps) != 2 || report.Steps[1].Files[0].Action != ActionOverwrite {
		t.Fatalf("report=%+v", report)
	}
	first, err := os.ReadFile(filepath.Join(root, ".spinup/plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(first) || bytes.Contains(first, []byte("Data")) || bytes.Contains(first, []byte("third")) {
		t.Fatalf("plan JSON: %s", first)
	}
	if !strings.Contains(r.Out.(*bytes.Buffer).String(), "Kit kit-note [manual]: Review this change") {
		t.Fatalf("output: %s", r.Out)
	}
	if _, err := r.Plan(context.Background(), Options{To: "0.3.0"}); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(root, ".spinup/plan.json"))
	if !bytes.Equal(first, second) {
		t.Fatal("plan JSON is not stable")
	}
	if got := testGit(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("plan changed project: %s", got)
	}
	if err := r.Run(context.Background(), Options{To: "0.3.0", Yes: true, NoVerify: true}); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, ".spinup/upgrade/0.3.0")
	lock, err := manifest.Load(filepath.Join(work, "spinup.lock"))
	if err != nil || lock.TemplateVersion != "0.3.0" {
		t.Fatalf("lock=%v error=%v", lock, err)
	}
	if got := testGit(t, work, "log", "--format=%s"); !strings.Contains(got, "spinup upgrade 0.2.0 -> 0.3.0") {
		t.Fatalf("chain log: %s", got)
	}
}

func TestRunPreconditions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name              string
		dirty, allow, yes bool
		want              string
	}{
		{name: "no confirmation", want: "--yes"},
		{name: "dirty", dirty: true, yes: true, want: "--allow-dirty"},
		{name: "allowed dirty", dirty: true, allow: true, yes: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, root := runFixture(t, false)
			if tc.dirty {
				writePlanFile(t, root, "other.txt", "user data", 0644)
			}
			err := r.Run(context.Background(), Options{To: "0.2.0", Yes: tc.yes, AllowDirty: tc.allow, NoVerify: true})
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error=%v", err)
				}
				if _, err := os.Stat(statePath(root)); !os.IsNotExist(err) {
					t.Fatalf("state written: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunPhaseOrderAndRecovery(t *testing.T) {
	// The phase registry is global. These tests must remain serial.
	old := CurrentPhases()
	defer SetPhases(old)
	oldExtensions := Extensions()
	hooksMu.Lock()
	extensions = nil
	hooksMu.Unlock()
	defer func() { hooksMu.Lock(); extensions = oldExtensions; hooksMu.Unlock() }()
	var calls []string
	RegisterExtension(Extension{Name: "test-extension", Run: func(_ context.Context, s *Step) error {
		calls = append(calls, "extension")
		writePlanFile(t, s.Worktree, "extension.txt", "extended\n", 0644)
		return nil
	}})
	phase := func(name string) func(context.Context, *Step) error {
		return func(_ context.Context, s *Step) error {
			calls = append(calls, name)
			if s.Worktree == "" || s.Lock == nil {
				t.Fatal("incomplete step")
			}
			return nil
		}
	}
	verifyFailed := false
	SetPhases(Phases{
		Before: phase("before"), Apply: phase("apply"), Deps: phase("deps"),
		GoFix: func(_ context.Context, s *Step) (bool, error) {
			calls = append(calls, "gofix")
			writePlanFile(t, s.Worktree, "fix.txt", "fixed\n", 0644)
			return true, nil
		},
		After: func(_ context.Context, s *Step) error {
			calls = append(calls, "after")
			s.Lock.AppliedMigrations = append(s.Lock.AppliedMigrations, "test-migration")
			return nil
		},
		Verify: func(context.Context, *Step) error {
			calls = append(calls, "verify")
			if !verifyFailed {
				verifyFailed = true
				return errors.New("verification failed")
			}
			return nil
		},
	})
	r, root := runFixture(t, false)
	ctx := context.Background()
	if err := r.Run(ctx, Options{To: "0.2.0", Yes: true}); err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("error=%v", err)
	}
	if err := r.Continue(ctx, Options{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"before", "apply", "deps", "gofix", "after", "extension", "verify", "verify"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v", calls)
	}
	work := filepath.Join(root, ".spinup/upgrade/0.2.0")
	lock, err := manifest.Load(filepath.Join(work, "spinup.lock"))
	if err != nil || !reflect.DeepEqual(lock.AppliedMigrations, []string{"test-migration"}) {
		t.Fatalf("lock=%v error=%v", lock, err)
	}
	if log := testGit(t, work, "log", "--format=%s"); !strings.Contains(log, "spinup upgrade: go fix") {
		t.Fatalf("log=%s", log)
	}
}

func TestRunCommitConflicts(t *testing.T) {
	t.Parallel()
	r, root := runFixture(t, true)
	if err := r.Run(context.Background(), Options{To: "0.2.0", Yes: true, NoVerify: true, CommitConflicts: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".spinup/upgrade/0.2.0/file.txt"))
	if err != nil || !bytes.Contains(data, []byte("<<<<<<<")) {
		t.Fatalf("conflicts=%s err=%v", data, err)
	}
}

func TestWorktreeFakeGit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	fake := &exec.Fake{}
	fake.Expect("git", "rev-parse", "--show-toplevel").Return(root+"\n", "", 0)
	r := &Runner{Root: root, Exec: fake}
	if err := r.checkRepository(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.Expect("git", "check-ignore").Return("", "", 1)
	if err := r.checkIgnored(context.Background(), root); err == nil || !strings.Contains(err.Error(), ".gitignore") {
		t.Fatalf("error=%v", err)
	}
	fake.AssertDone(t)
}

func TestStateValidation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, tc := range []struct {
		name   string
		modify func(*runState)
	}{
		{name: "index", modify: func(s *runState) { s.Index = 3 }},
		{name: "path", modify: func(s *runState) { s.Worktree = root }},
		{name: "branch", modify: func(s *runState) { s.Branch = "main" }},
		{name: "status", modify: func(s *runState) { s.Status = "invalid" }},
		{name: "conflict path", modify: func(s *runState) { s.Conflicts = []string{"../escape"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &runState{Schema: 1, Chain: []string{"0.2.0"}, Worktree: filepath.Join(root, ".spinup/upgrade/0.2.0"), Branch: "spinup/upgrade-0.2.0", Status: "step", Lock: planLock("old", "scaffold")}
			if err := s.validate(root); err != nil {
				t.Fatal(err)
			}
			tc.modify(s)
			if err := s.validate(root); err == nil {
				t.Fatal("unsafe state accepted")
			}
		})
	}
}

func TestRunWritePlan(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePlanFile(t, root, "old.txt", "old", 0644)
	writePlanFile(t, root, "delete.txt", "delete", 0644)
	writePlanFile(t, root, "keep.txt", "user", 0600)
	plan := &Plan{Files: []FileDecision{{Path: "new.txt", From: "old.txt", Action: ActionRename, Data: []byte("new"), Mode: 0755}, {Path: "delete.txt", Action: ActionDelete}, {Path: "keep.txt", Action: ActionKeep, Data: []byte("wrong"), Mode: 0755}}}
	if err := writePlan(root, plan); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"old.txt", "delete.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("%s remains", name)
		}
	}
	info, err := os.Stat(filepath.Join(root, "new.txt"))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("mode: %v %v", info, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "keep.txt"))
	if string(data) != "user" {
		t.Fatalf("keep changed: %s", data)
	}
	info, err = os.Stat(filepath.Join(root, "keep.txt"))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("keep mode: %v %v", info, err)
	}
	if err := writePlan(root, &Plan{Files: []FileDecision{{Path: "../escape", Action: ActionCreate}}}); err == nil {
		t.Fatal("unsafe path accepted")
	}
}

func TestStateConflictMarkers(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"<<<<<<< ours", "=======", ">>>>>>> theirs"} {
		t.Run(prefix, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writePlanFile(t, root, "file.txt", "resolved line\n"+prefix+"\n", 0644)
			paths, err := unresolved(root, []string{"file.txt"})
			if err != nil || !reflect.DeepEqual(paths, []string{"file.txt"}) {
				t.Fatalf("paths=%v err=%v", paths, err)
			}
		})
	}
}

func TestWorktreeGitVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		version string
		fail    bool
	}{
		{"git version 2.44.0\n", false}, {"git version 2.50.1 (Apple Git-155)\n", false}, {"git version 2.43.9\n", true}, {"unexpected", true},
	} {
		t.Run(tc.version, func(t *testing.T) {
			t.Parallel()
			fake := &exec.Fake{}
			fake.Expect("git", "--version").Return(tc.version, "", 0)
			_, err := merge.CheckGitVersion(context.Background(), fake)
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v", err)
			}
			fake.AssertDone(t)
		})
	}
}

func TestRunMissingBase(t *testing.T) {
	t.Parallel()
	for _, edited := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "edited"}[edited], func(t *testing.T) {
			t.Parallel()
			r, _ := runFixture(t, edited)
			delete(r.Source.(chainSource), "0.1.0")
			err := r.Run(context.Background(), Options{To: "0.2.0", Yes: true, NoVerify: true})
			if edited {
				if err == nil || !strings.Contains(err.Error(), "file.txt") {
					t.Fatalf("error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
