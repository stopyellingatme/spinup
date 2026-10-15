package upgrade

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/migrate"
)

func TestPhaseMigrations(t *testing.T) {
	t.Parallel()
	s := phaseStep(t)
	s.From = nil
	var out bytes.Buffer
	s.Out = &out
	registry := &migrate.Registry{}
	var calls []string
	for _, stage := range []migrate.Stage{migrate.Before, migrate.After} {
		id := "before"
		if stage == migrate.After {
			id = "after"
		}
		registry.Register(migrate.Migration{
			ID: id, Since: "0.2.0", Desc: "Test " + id, Stage: stage,
			Plan: func(c *migrate.Ctx) ([]migrate.Change, error) {
				if c.Root != s.Worktree || c.From != "0.1.0" || c.To != "0.2.0" || c.Runner != s.Runner || c.Lock != s.Lock {
					t.Fatalf("migration context=%+v", c)
				}
				return []migrate.Change{{Path: "api/main.go"}}, nil
			},
			Apply: func(c *migrate.Ctx) error {
				calls = append(calls, id)
				if stage == migrate.Before {
					c.Answers = map[string]any{"changed": true}
				}
				return nil
			},
		})
	}
	p := newPhaseHooks(registry.Pending, doctor.RunAll)
	for _, phase := range []func(context.Context, *Step) error{p.Before, p.Before, p.After, p.After} {
		if err := phase(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(calls, []string{"before", "after"}) || !reflect.DeepEqual(s.Lock.AppliedMigrations, calls) {
		t.Fatalf("calls=%v applied=%v", calls, s.Lock.AppliedMigrations)
	}
	if s.Answers["changed"] != true || strings.Count(out.String(), "Migration ") != 2 {
		t.Fatalf("answers=%v output=%s", s.Answers, &out)
	}
}

func TestPhaseMigrationPartialFailure(t *testing.T) {
	t.Parallel()
	s := phaseStep(t)
	r := &migrate.Registry{}
	for _, id := range []string{"a", "b"} {
		r.Register(migrate.Migration{ID: id, Since: "0.2.0", Stage: migrate.Before,
			Plan: func(*migrate.Ctx) ([]migrate.Change, error) { return nil, nil },
			Apply: func(c *migrate.Ctx) error {
				c.Answers = map[string]any{"partial": true}
				if id == "b" {
					return errors.New("cannot change file")
				}
				return nil
			},
		})
	}
	p := newPhaseHooks(r.Pending, doctor.RunAll)
	err := p.Before(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), "cannot change file") || !reflect.DeepEqual(s.Lock.AppliedMigrations, []string{"a"}) || s.Answers["partial"] != true {
		t.Fatalf("error=%v ids=%v answers=%v", err, s.Lock.AppliedMigrations, s.Answers)
	}
}

func TestVerifyCollectsFailuresAndMigrationIDs(t *testing.T) {
	t.Parallel()
	for _, resumed := range []bool{false, true} {
		t.Run(map[bool]string{false: "same process", true: "continued process"}[resumed], func(t *testing.T) {
			t.Parallel()
			s := phaseStep(t)
			r := &migrate.Registry{}
			for _, id := range []string{"a", "b"} {
				r.Register(migrate.Migration{ID: id, Since: "0.2.0", Stage: migrate.After,
					Plan: func(*migrate.Ctx) ([]migrate.Change, error) {
						return []migrate.Change{{Path: "api/cmd/api/main.go"}}, nil
					}, Apply: func(*migrate.Ctx) error { return nil },
				})
			}
			doctorCalls := 0
			doctorRun := func(_ context.Context, scope doctor.Scope, c *doctor.Context) []doctor.Report {
				doctorCalls++
				if scope != doctor.Local || c.Root != s.Worktree || c.Deps["runner"] != s.Runner {
					t.Fatalf("doctor context=%+v scope=%v", c, scope)
				}
				return []doctor.Report{
					{Check: doctor.Check{ID: "broken"}, Result: doctor.Result{Status: doctor.Fail, Message: "api/cmd/api/main.go is broken"}},
					{Check: doctor.Check{ID: "warning"}, Result: doctor.Result{Status: doctor.Warn, Message: "warning only"}},
				}
			}
			p := newPhaseHooks(r.Pending, doctorRun)
			if err := p.After(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			if resumed {
				p = newPhaseHooks(r.Pending, doctorRun)
			}
			fake := s.Runner.(*exec.Fake)
			fake.Expect("go", "build", "./...").Return("", "cmd/api/main.go:4: undefined: missing", 1)
			fake.Expect("go", "vet", "./...").Return("", "./cmd/api/main.go:4: undefined: missing", 1)
			fake.Expect("go", "test", "./...").Return("FAIL example.com/demo/api\n", "tests failed", 1)
			err := p.Verify(context.Background(), s)
			for _, want := range []string{"go build ./...", "go vet ./...", "go test ./...", "doctor broken", "migration b", "Try: fix the failures in " + s.Worktree + ", then run spinup upgrade --continue"} {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("missing %q in error=%v", want, err)
				}
			}
			if strings.Contains(err.Error(), "warning only") || doctorCalls != 1 {
				t.Fatalf("error=%v doctor calls=%d", err, doctorCalls)
			}
			api := filepath.Join(s.Worktree, "api")
			assertPhaseCalls(t, fake, []string{api, api, api}, "go build ./...", "go vet ./...", "go test ./...")
		})
	}
}

func TestVerifySuccess(t *testing.T) {
	t.Parallel()
	s := phaseStep(t)
	fake := s.Runner.(*exec.Fake)
	for _, command := range []string{"build", "vet", "test"} {
		fake.Expect("go", command, "./...")
	}
	r := &migrate.Registry{}
	p := newPhaseHooks(r.Pending, func(context.Context, doctor.Scope, *doctor.Context) []doctor.Report { return nil })
	if err := p.Verify(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	fake.AssertDone(t)
}

func TestApplyKitReplace(t *testing.T) {
	// Setenv changes the process environment. Keep this test serial.
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "unset", true: "local kit"}[replace], func(t *testing.T) {
			s := phaseStep(t)
			kit := ""
			if replace {
				kit = t.TempDir()
			}
			t.Setenv("SPINUP_KIT_REPLACE", kit)
			fake := s.Runner.(*exec.Fake)
			if replace {
				fake.Expect("go", "mod", "edit", "-replace="+phaseKit+"="+filepath.ToSlash(kit))
			}
			if err := phaseApply(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			fake.AssertDone(t)
			if replace {
				assertPhaseCalls(t, fake, []string{filepath.Join(s.Worktree, "api")}, "go mod edit -replace="+phaseKit+"="+filepath.ToSlash(kit))
			}
		})
	}
}

func TestApplyKitReplaceIdempotent(t *testing.T) {
	s := phaseStep(t)
	kit := filepath.Join(t.TempDir(), "local kit")
	if err := os.Mkdir(kit, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPINUP_KIT_REPLACE", kit)
	s.Runner = exec.OSRunner{}
	if err := phaseApply(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(s.Worktree, "api/go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), `replace `+phaseKit+` => "`+filepath.ToSlash(kit)+`"`) {
		t.Fatalf("local replacement is missing or unquoted: %s", first)
	}
	if err := phaseApply(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(s.Worktree, "api/go.mod"))
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("replacement is not idempotent: %s error=%v", second, err)
	}
}

func TestPhasesWithoutAPI(t *testing.T) {
	t.Parallel()
	s := phaseStep(t)
	if err := os.Remove(filepath.Join(s.Worktree, "api/go.mod")); err != nil {
		t.Fatal(err)
	}
	if err := phaseDeps(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	changed, err := phaseGoFix(context.Background(), s)
	if changed || err != nil {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if calls := s.Runner.(*exec.Fake).Calls(); len(calls) != 0 {
		t.Fatalf("unexpected subprocesses: %v", calls)
	}
}
