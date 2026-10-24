package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func writeAPIMod(t *testing.T, root, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "api/go.mod"), []byte("module example.com/demo/api\n"+body), 0644); err != nil {
		t.Fatal(err)
	}
}

// These tests change the process environment, so they run serially.
func TestApplyKeepsCurrentKitReplace(t *testing.T) {
	t.Run("env unset, replace to an existing directory", func(t *testing.T) {
		t.Setenv("SPINUP_KIT_REPLACE", "")
		s := phaseStep(t)
		kit := t.TempDir()
		writeAPIMod(t, s.Worktree, "\nreplace "+phaseKit+" => "+kit+"\n")
		fake := s.Runner.(*exec.Fake)
		fake.Expect("go", "mod", "edit", "-replace="+phaseKit+"="+filepath.ToSlash(kit))
		if err := phaseApply(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		fake.AssertDone(t)
	})
	t.Run("relative replace is read from the main checkout", func(t *testing.T) {
		t.Setenv("SPINUP_KIT_REPLACE", "")
		s := phaseStep(t)
		main := s.Worktree
		kit := filepath.Join(main, "kit")
		if err := os.Mkdir(kit, 0755); err != nil {
			t.Fatal(err)
		}
		writeAPIMod(t, main, "\nreplace "+phaseKit+" => ../kit\n")
		work := filepath.Join(main, ".spinup", "upgrade", "0.2.0")
		if err := os.MkdirAll(filepath.Join(work, "api"), 0755); err != nil {
			t.Fatal(err)
		}
		// The go.mod of the worktree has lost the replace.
		writeAPIMod(t, work, "")
		s.Worktree = work
		fake := s.Runner.(*exec.Fake)
		fake.Expect("go", "mod", "edit", "-replace="+phaseKit+"="+filepath.ToSlash(kit))
		if err := phaseApply(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		fake.AssertDone(t)
	})
	t.Run("env set wins over the current replace", func(t *testing.T) {
		envKit := t.TempDir()
		t.Setenv("SPINUP_KIT_REPLACE", envKit)
		s := phaseStep(t)
		writeAPIMod(t, s.Worktree, "\nreplace "+phaseKit+" => "+t.TempDir()+"\n")
		fake := s.Runner.(*exec.Fake)
		fake.Expect("go", "mod", "edit", "-replace="+phaseKit+"="+filepath.ToSlash(envKit))
		if err := phaseApply(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		fake.AssertDone(t)
	})
	t.Run("env unset, replace to a missing directory is an error", func(t *testing.T) {
		t.Setenv("SPINUP_KIT_REPLACE", "")
		s := phaseStep(t)
		missing := filepath.Join(t.TempDir(), "gone")
		writeAPIMod(t, s.Worktree, "\nreplace "+phaseKit+" => "+missing+"\n")
		err := phaseApply(context.Background(), s)
		var ue *ui.Error
		if !errors.As(err, &ue) || ue.Try == "" {
			t.Fatalf("error = %v", err)
		}
		if n := len(s.Runner.(*exec.Fake).Calls()); n != 0 {
			t.Fatalf("calls = %d", n)
		}
	})
	t.Run("env unset, no replace", func(t *testing.T) {
		t.Setenv("SPINUP_KIT_REPLACE", "")
		s := phaseStep(t)
		if err := phaseApply(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		if n := len(s.Runner.(*exec.Fake).Calls()); n != 0 {
			t.Fatalf("calls = %d", n)
		}
	})
}
