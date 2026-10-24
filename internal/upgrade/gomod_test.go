package upgrade

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/stackversions"
)

func TestPlanKeepsChangedGoModFiles(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"api/go.mod", "api/go.sum"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writePlanFile(t, root, name, "app tidy\n", 0o644)
			lock := planLock("old\n", "")
			lock.Files[name] = manifest.FileEntry{Class: manifest.Scaffold, Template: "base/file0.tmpl", Hash: manifest.Hash([]byte("old\n")), Mode: "0644"}
			older := planVersion("0.1.0", planSpec{path: name, data: "old\n"})
			newer := planVersion("0.2.0", planSpec{path: name, data: "new\n"})
			merger := &planMerger{}
			p, err := BuildPlan(context.Background(), PlanInput{Root: root, Lock: lock, Old: older, New: newer, Merger: merger})
			if err != nil {
				t.Fatal(err)
			}
			d := p.Files[0]
			if d.Action != ActionKeep || d.Reason != goModKeepReason || len(merger.inputs) != 0 {
				t.Fatalf("decision %#v merges=%d, want keep without merge", d, len(merger.inputs))
			}
			if string(d.Data) != "new\n" {
				t.Errorf("data=%q, want the new render", d.Data)
			}
			if len(p.Conflicts()) != 0 {
				t.Errorf("conflicts=%v", p.Conflicts())
			}
			if err := requireBase(p, nil, "0.1.0"); err != nil {
				t.Errorf("requireBase fired for a kept file: %v", err)
			}
		})
	}
}

func TestPlanOverwritesUnchangedGoMod(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePlanFile(t, root, "api/go.mod", "old\n", 0o644)
	lock := planLock("old\n", "")
	lock.Files["api/go.mod"] = manifest.FileEntry{Class: manifest.Scaffold, Template: "base/file0.tmpl", Hash: manifest.Hash([]byte("old\n")), Mode: "0644"}
	p, err := BuildPlan(context.Background(), PlanInput{Root: root, Lock: lock, Old: planVersion("0.1.0", planSpec{path: "api/go.mod", data: "old\n"}), New: planVersion("0.2.0", planSpec{path: "api/go.mod", data: "new\n"}), Merger: &planMerger{}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Files[0].Action != ActionOverwrite {
		t.Fatalf("decision %#v, want overwrite", p.Files[0])
	}
}

func TestDepsRaisesKitPinAndGoDirective(t *testing.T) {
	t.Parallel()
	s := phaseStep(t)
	s.To.Version = "0.1.0-alpha.428"
	s.To.Stack = &stackversions.Stack{GoModules: map[string]string{phaseKit: "v0.1.0-alpha.418"}}
	s.Plan = &Plan{Files: []FileDecision{{Path: "api/go.mod", Action: ActionKeep, Reason: goModKeepReason, Data: []byte("module example.com/demo/api\n\ngo 1.27.0\n")}}}
	fake := s.Runner.(*exec.Fake)
	fake.Expect("go", "mod", "edit", "-json").Return(`{"Require":[{"Path":"`+phaseKit+`","Version":"v0.1.0-alpha.400"}]}`, "", 0)
	fake.Expect("go", "mod", "edit", "-require="+phaseKit+"@v0.1.0-alpha.418")
	fake.Expect("go", "mod", "edit", "-go=1.27.0")
	fake.Expect("go", "mod", "tidy")
	if err := phaseDeps(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if s.Lock.Kit.Version != "v0.1.0-alpha.418" {
		t.Fatalf("kit lock=%+v", s.Lock.Kit)
	}
	api := filepath.Join(s.Worktree, "api")
	assertPhaseCalls(t, fake, []string{api, api, api, api}, "go mod edit -json", "go mod edit -require="+phaseKit+"@v0.1.0-alpha.418", "go mod edit -go=1.27.0", "go mod tidy")
}

func TestDepsKeepsHigherKitAndGoDirective(t *testing.T) {
	t.Parallel()
	s := phaseStep(t)
	s.To.Version = "0.1.0-alpha.428"
	s.To.Stack = &stackversions.Stack{GoModules: map[string]string{phaseKit: "v0.1.0-alpha.418"}}
	s.Plan = &Plan{Files: []FileDecision{{Path: "api/go.mod", Action: ActionKeep, Data: []byte("module x\n\ngo 1.25\n")}}}
	fake := s.Runner.(*exec.Fake)
	fake.Expect("go", "mod", "edit", "-json").Return(`{"Require":[{"Path":"`+phaseKit+`","Version":"v0.1.1"}]}`, "", 0)
	fake.Expect("go", "mod", "tidy")
	if err := phaseDeps(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	api := filepath.Join(s.Worktree, "api")
	assertPhaseCalls(t, fake, []string{api, api}, "go mod edit -json", "go mod tidy")
}

func TestDepsReplacesThePlaceholderKit(t *testing.T) {
	t.Parallel()
	// Templates required the unpublished v0.1.0 before ADR 0018. It is higher than the pinned
	// pre-release in semver, but it never existed, so the pin replaces it.
	s := phaseStep(t)
	s.To.Version = "0.1.0-alpha.428"
	s.To.Stack = &stackversions.Stack{GoModules: map[string]string{phaseKit: "v0.1.0-alpha.418"}}
	s.Plan = &Plan{Files: []FileDecision{{Path: "api/go.mod", Action: ActionKeep, Data: []byte("module x\n\ngo 1.26.0\n")}}}
	fake := s.Runner.(*exec.Fake)
	fake.Expect("go", "mod", "edit", "-json").Return(`{"Require":[{"Path":"`+phaseKit+`","Version":"v0.1.0"}]}`, "", 0)
	fake.Expect("go", "mod", "edit", "-require="+phaseKit+"@v0.1.0-alpha.418")
	fake.Expect("go", "mod", "tidy")
	if err := phaseDeps(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	fake.AssertDone(t)
	if s.Lock.Kit.Version != "v0.1.0-alpha.418" {
		t.Fatalf("kit lock=%+v", s.Lock.Kit)
	}
}

func TestPhaseErrorKeepsInnerTry(t *testing.T) {
	t.Parallel()
	inner := errors.New("api/go.mod has merge conflict markers\nTry: resolve the conflict in that file")
	err := phaseError("phase", inner)
	if got := strings.Count(err.Error(), "Try:"); got != 1 || !strings.Contains(err.Error(), "Try: resolve the conflict") {
		t.Fatalf("error=%q", err)
	}
	plain := phaseError("phase", errors.New("boom"))
	if got := strings.Count(plain.Error(), "Try:"); got != 1 || !strings.Contains(plain.Error(), "--continue or --abort") {
		t.Fatalf("error=%q", plain)
	}
}
