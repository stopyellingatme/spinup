package migratetest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/migrate"
)

type recordingTB struct {
	testing.TB
	messages []string
}

func (t *recordingTB) Errorf(format string, args ...any) {
	t.messages = append(t.messages, fmt.Sprintf(format, args...))
}

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRunDetectsViolations(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"plan", "apply", "runner"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			root := fixture(t)
			record := &recordingTB{TB: t}
			calls := 0
			m := migrate.Migration{ID: "example", Since: "0.2.0", Plan: func(c *migrate.Ctx) ([]migrate.Change, error) {
				if phase == "plan" {
					return nil, os.WriteFile(filepath.Join(c.Root, "file"), []byte("changed\n"), 0o644)
				}
				return nil, nil
			}, Apply: func(c *migrate.Ctx) error {
				if phase == "apply" {
					calls++
					return os.WriteFile(filepath.Join(c.Root, "file"), fmt.Appendf(nil, "call %d\n", calls), 0o644)
				}
				if phase == "runner" {
					_, _ = c.Runner.Run(context.Background(), exec.Cmd{Name: "forbidden"})
				}
				return nil
			}}
			Run(record, m, root, root)
			joined := strings.Join(record.messages, "\n")
			want := map[string]string{"plan": "Plan changed the tree", "apply": "Apply is not idempotent", "runner": "unexpected subprocess"}[phase]
			if !strings.Contains(joined, want) {
				t.Fatalf("missing %q in %s", want, joined)
			}
		})
	}
}

func TestWithRunner(t *testing.T) {
	t.Parallel()
	root := fixture(t)
	fake := &exec.Fake{}
	for range 3 {
		fake.Expect("allowed")
	}
	call := func(c *migrate.Ctx) error {
		_, err := c.Runner.Run(context.Background(), exec.Cmd{Name: "allowed"})
		return err
	}
	m := migrate.Migration{ID: "runner", Since: "0.2.0", Plan: func(c *migrate.Ctx) ([]migrate.Change, error) { return nil, call(c) }, Apply: call}
	Run(t, m, root, root, WithRunner(fake))
	fake.AssertDone(t)
}
