package upgrade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stopyellingatme/spinup/internal/exec"
)

const kitModule = "github.com/stopyellingatme/spinup/kit"

func phaseApply(ctx context.Context, s *Step) error {
	replace := os.Getenv("SPINUP_KIT_REPLACE")
	if replace == "" {
		return nil
	}
	hasAPI, err := phaseHasAPI(s)
	if err != nil || !hasAPI {
		return err
	}
	replace, err = filepath.Abs(replace)
	if err != nil {
		return fmt.Errorf("resolve local kit replace: %w\nTry: set SPINUP_KIT_REPLACE to the kit directory", err)
	}
	info, err := os.Stat(replace)
	if err != nil {
		return fmt.Errorf("inspect local kit replace %q: %w\nTry: set SPINUP_KIT_REPLACE to the kit directory", replace, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("local kit replace %q is not a directory\nTry: set SPINUP_KIT_REPLACE to the kit directory", replace)
	}
	// go mod edit quotes the path and replaces an existing directive idempotently.
	_, err = phaseGo(ctx, s, "mod", "edit", "-replace="+kitModule+"="+filepath.ToSlash(replace))
	return err
}

func phaseHasAPI(s *Step) (bool, error) {
	_, err := os.Stat(filepath.Join(s.Worktree, "api", "go.mod"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect api/go.mod: %w\nTry: check the upgrade worktree permissions", err)
	}
	return true, nil
}

func phaseGo(ctx context.Context, s *Step, args ...string) (exec.Result, error) {
	command := exec.Cmd{Name: "go", Args: args, Dir: filepath.Join(s.Worktree, "api")}
	result, err := s.Runner.Run(ctx, command)
	if err != nil {
		return result, fmt.Errorf("run %s in %s: %w\nTry: correct the failure in the upgrade worktree and run spinup upgrade --continue", command.String(), command.Dir, err)
	}
	return result, nil
}
