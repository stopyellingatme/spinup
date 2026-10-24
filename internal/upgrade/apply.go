package upgrade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stopyellingatme/spinup/internal/build"
	"github.com/stopyellingatme/spinup/internal/exec"
)

const kitModule = "github.com/stopyellingatme/spinup/kit"

func phaseApply(ctx context.Context, s *Step) error {
	hasAPI, err := phaseHasAPI(s)
	if err != nil || !hasAPI {
		return err
	}
	replace := os.Getenv("SPINUP_KIT_REPLACE")
	if replace == "" {
		// The variable wins when it is set. Without it, keep the replace of the current api/go.mod.
		dir, rel, err := currentKitReplace(s)
		if err != nil || dir == "" {
			return err
		}
		if rel != "" {
			// The worktree is a checkout of the same repository. A relative path to a directory
			// in the repository resolves there too, so the text of the replace stays as it is.
			if info, err := os.Stat(filepath.Join(s.Worktree, "api", filepath.FromSlash(rel))); err == nil && info.IsDir() {
				_, err = phaseGo(ctx, s, "mod", "edit", "-replace="+kitModule+"="+rel)
				return err
			}
			if s.Out != nil {
				_, _ = fmt.Fprintf(s.Out, "Warning: the kit replace %s points outside the repository, so the upgrade writes the absolute path %s. Write %s again in api/go.mod after the merge.\n", rel, filepath.ToSlash(dir), rel)
			}
		}
		replace = dir
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

// currentKitReplace returns the kit directory that the current api/go.mod replaces, or "".
// It also returns the path as written when that path is relative.
// The upgrade worktree is <root>/.spinup/upgrade/<version>. A relative replace path is
// relative to the api directory of the main checkout, so the function reads that go.mod.
// A replace to a missing directory is an error, because go mod tidy would fail on it.
func currentKitReplace(s *Step) (string, string, error) {
	api := filepath.Join(s.Worktree, "api")
	if state := filepath.Dir(filepath.Dir(s.Worktree)); filepath.Base(state) == ".spinup" {
		main := filepath.Join(filepath.Dir(state), "api")
		if _, err := os.Stat(filepath.Join(main, "go.mod")); err == nil {
			api = main
		}
	}
	dir, ok, err := build.KitReplaceDir(api)
	if err != nil || !ok {
		return "", "", err
	}
	target, _, err := build.KitReplaceTarget(api)
	if err != nil || filepath.IsAbs(filepath.FromSlash(target)) {
		return dir, "", err
	}
	return dir, target, nil
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
