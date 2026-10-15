package upgrade

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
)

const phaseKit = "github.com/stopyellingatme/spinup/kit"

func phaseStep(t *testing.T) *Step {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "api"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api/go.mod"), []byte("module example.com/demo/api\n\ngo 1.26.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return &Step{
		From:     &templates.Version{Version: "0.1.0"},
		To:       &templates.Version{Version: "0.2.0", Stack: &stackversions.Stack{GoModules: map[string]string{}}},
		Worktree: root, Lock: &manifest.Lock{TemplateVersion: "0.1.0"},
		Runner: &exec.Fake{}, Out: io.Discard,
	}
}

func assertPhaseCalls(t *testing.T, fake *exec.Fake, dirs []string, commands ...string) {
	t.Helper()
	fake.AssertDone(t)
	calls := fake.Calls()
	if len(calls) != len(commands) {
		t.Fatalf("got %d calls, want %d: %+v", len(calls), len(commands), calls)
	}
	for i, call := range calls {
		if call.String() != commands[i] || call.Dir != dirs[i] || len(call.Env) != 0 {
			t.Errorf("call %d: %s in %s env=%v, want %s in %s", i, call.String(), call.Dir, call.Env, commands[i], dirs[i])
		}
	}
}

func TestDepsPinsOnlyDirectLowerRequirements(t *testing.T) {
	t.Parallel()
	s := phaseStep(t)
	s.To.Stack.GoModules = map[string]string{
		"example.com/z": "v1.3.0", "example.com/a": "v1.10.0", "example.com/newer": "v1.0.0",
		"example.com/indirect": "v2.0.0", "example.com/absent": "v1.0.0", "example.com/equal": "v1.0.0",
		"example.com/pre": "v1.0.0", phaseKit: "v9.0.0",
	}
	fake := s.Runner.(*exec.Fake)
	fake.Expect("go", "mod", "edit", "-json").Return(`{"Require":[
		{"Path":"example.com/z","Version":"v1.2.0"},
		{"Path":"example.com/newer","Version":"v1.1.0"},
		{"Path":"example.com/a","Version":"v1.9.0"},
		{"Path":"example.com/equal","Version":"v1.0.0"},
		{"Path":"example.com/indirect","Version":"v1.0.0","Indirect":true},
		{"Path":"example.com/pre","Version":"v1.0.0-rc.1"},
		{"Path":"`+phaseKit+`","Version":"v0.1.0"}]}`, "", 0)
	commands := []string{"go mod edit -json"}
	for _, pin := range []string{"example.com/a@v1.10.0", "example.com/pre@v1.0.0", "example.com/z@v1.3.0", phaseKit + "@v0.2.0"} {
		fake.Expect("go", "mod", "edit", "-require="+pin)
		commands = append(commands, "go mod edit -require="+pin)
	}
	fake.Expect("go", "mod", "tidy")
	commands = append(commands, "go mod tidy")
	var out bytes.Buffer
	s.Out = &out
	if err := phaseDeps(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if s.Lock.Kit.Module != phaseKit || s.Lock.Kit.Version != "v0.2.0" {
		t.Fatalf("kit lock=%+v", s.Lock.Kit)
	}
	dirs := make([]string, len(commands))
	for i := range dirs {
		dirs[i] = filepath.Join(s.Worktree, "api")
	}
	assertPhaseCalls(t, fake, dirs, commands...)
	for _, name := range []string{"example.com/a", "example.com/pre", "example.com/z", phaseKit} {
		if !strings.Contains(out.String(), name) {
			t.Errorf("missing bump %s: %s", name, &out)
		}
	}
}

func TestDepsPrereleaseAndTidyFailure(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"0.0.0-dev", "0.2.0-rc.1"} {
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			s := phaseStep(t)
			s.To.Version = version
			fake := s.Runner.(*exec.Fake)
			fake.Expect("go", "mod", "edit", "-json").Return(`{"Require":[{"Path":"`+phaseKit+`","Version":"v0.1.0"}]}`, "", 0)
			fake.Expect("go", "mod", "tidy").Return("", "network unavailable", 1)
			err := phaseDeps(context.Background(), s)
			if err == nil || !strings.Contains(err.Error(), "go mod tidy") || !strings.HasSuffix(err.Error(), "Try: rerun with network access, or pass --no-verify and fix go.mod by hand") {
				t.Fatalf("error=%v", err)
			}
			api := filepath.Join(s.Worktree, "api")
			assertPhaseCalls(t, fake, []string{api, api}, "go mod edit -json", "go mod tidy")
		})
	}
}

func TestGoFixChanges(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"", " M api/main.go\n", "?? api/new.go\n"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			s := phaseStep(t)
			fake := s.Runner.(*exec.Fake)
			fake.Expect("go", "fix", "./...")
			fake.Expect("git", "status", "--porcelain", "--", "api/").Return(status, "", 0)
			changed, err := phaseGoFix(context.Background(), s)
			if err != nil || changed != (status != "") {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			assertPhaseCalls(t, fake, []string{filepath.Join(s.Worktree, "api"), s.Worktree}, "go fix ./...", "git status --porcelain -- api/")
		})
	}
}

func TestGoFixFailures(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"go", "git"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			s := phaseStep(t)
			fake := s.Runner.(*exec.Fake)
			fix := fake.Expect("go", "fix", "./...")
			if command == "go" {
				fix.Return("", "cannot fix code", 1)
			} else {
				fake.Expect("git", "status", "--porcelain", "--", "api/").Return("", "cannot inspect status", 1)
			}
			changed, err := phaseGoFix(context.Background(), s)
			if changed || err == nil || !strings.Contains(err.Error(), command) || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			fake.AssertDone(t)
		})
	}
}

func TestDepsInvalidInput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, response, pin, want string
	}{
		{"json", "invalid", "v1.0.0", "decode go mod edit -json"},
		{"requirement", `{"Require":[{"Path":"example.com/a","Version":"invalid"}]}`, "v1.0.0", "compare dependency example.com/a"},
		{"pin", `{"Require":[{"Path":"example.com/a","Version":"v1.0.0"}]}`, "invalid", "compare dependency pin example.com/a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := phaseStep(t)
			s.To.Stack.GoModules["example.com/a"] = tc.pin
			fake := s.Runner.(*exec.Fake)
			fake.Expect("go", "mod", "edit", "-json").Return(tc.response, "", 0)
			err := phaseDeps(context.Background(), s)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("error=%v", err)
			}
			fake.AssertDone(t)
		})
	}
}

func TestPhasesInstalled(t *testing.T) {
	// The state machine tests replace the registry in serial tests.
	p := CurrentPhases()
	for _, phase := range []any{p.Before, p.Apply, p.Deps, p.GoFix, p.After, p.Verify} {
		if reflect.ValueOf(phase).IsNil() {
			t.Fatal("a phase was not installed")
		}
	}
}

func TestDepsNeverLowersKit(t *testing.T) {
	t.Parallel()
	s := phaseStep(t)
	s.To.Version = "0.0.1"
	fake := s.Runner.(*exec.Fake)
	fake.Expect("go", "mod", "edit", "-json").Return(`{"Require":[{"Path":"`+phaseKit+`","Version":"v0.1.0"}]}`, "", 0)
	fake.Expect("go", "mod", "tidy")
	if err := phaseDeps(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	api := filepath.Join(s.Worktree, "api")
	assertPhaseCalls(t, fake, []string{api, api}, "go mod edit -json", "go mod tidy")
	if s.Lock.Kit.Version != "" {
		t.Fatalf("kit lock changed to %+v", s.Lock.Kit)
	}
}

func TestGoFixAndVerifySkipCommittedConflicts(t *testing.T) {
	t.Parallel()
	s := phaseStep(t)
	s.Plan = &Plan{Files: []FileDecision{{Path: "api/main.go", Action: ActionConflict}}}
	var out bytes.Buffer
	s.Out = &out
	changed, err := phaseGoFix(context.Background(), s)
	if err != nil || changed {
		t.Fatalf("GoFix changed=%v err=%v", changed, err)
	}
	if err := (&phaseHooks{}).verify(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	s.Runner.(*exec.Fake).AssertDone(t)
	if !strings.Contains(out.String(), "Skipped go fix") || !strings.Contains(out.String(), "Skipped verify") {
		t.Fatalf("output=%q", out.String())
	}
}
