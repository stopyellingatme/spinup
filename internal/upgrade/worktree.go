package upgrade

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/merge"
)

// CheckGitVersion checks the minimum Git version. Tests may replace it.
var CheckGitVersion = func(ctx context.Context, runner exec.Runner) error {
	_, err := merge.CheckGitVersion(ctx, runner)
	return err
}

// NewMerger constructs the merger for an upgrade. Tests may replace it.
var NewMerger = merge.NewGit

func (r *Runner) git(ctx context.Context, dir string, args ...string) (string, error) {
	result, err := r.Exec.Run(ctx, exec.Cmd{Name: "git", Args: args, Dir: dir})
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w\nTry: correct the Git error, then run spinup upgrade --continue or --abort", strings.Join(args, " "), err)
	}
	return string(result.Stdout), nil
}

func (r *Runner) checkRepository(ctx context.Context) error {
	top, err := r.git(ctx, r.Root, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	top = strings.TrimSpace(top)
	if top != r.Root {
		return fmt.Errorf("upgrade repository %q: Git root is %q\nTry: run spinup upgrade at the project Git root", r.Root, top)
	}
	return nil
}

func (r *Runner) checkIgnored(ctx context.Context, dir string) error {
	if _, err := r.git(ctx, dir, "check-ignore", "-q", "--no-index", ".spinup/upgrade.json"); err != nil {
		return fmt.Errorf("check upgrade storage: local .spinup/ state must be ignored by Git\nTry: ignore .spinup/* except plugins.allow and plugins.history, then commit .gitignore")
	}
	tracked, err := r.git(ctx, dir, "ls-files", "--", ".spinup")
	if err != nil {
		return err
	}
	for _, name := range strings.Fields(tracked) {
		if name != ".spinup/plugins.allow" && name != ".spinup/plugins.history" {
			return fmt.Errorf("check upgrade storage: .spinup/ contains tracked state %q\nTry: remove local state from the Git index and commit the change", name)
		}
	}
	if _, err := r.git(ctx, dir, "check-ignore", "-q", "--no-index", ".spinup/upgrade/probe"); err != nil {
		return fmt.Errorf("check upgrade storage: .spinup/upgrade must be ignored by Git\nTry: ignore .spinup/* except plugins.allow and plugins.history, then commit .gitignore")
	}

	return nil
}

func (r *Runner) createWorktree(ctx context.Context, to string) (string, string, error) {
	work := filepath.Join(r.Root, ".spinup", "upgrade", to)
	branch := "spinup/upgrade-" + to
	if err := r.checkIgnored(ctx, r.Root); err != nil {
		return "", "", err
	}
	if _, err := r.git(ctx, r.Root, "worktree", "add", "-b", branch, work, "HEAD"); err != nil {
		return "", "", err
	}
	if err := r.checkIgnored(ctx, work); err != nil {
		_, removeErr := r.git(ctx, r.Root, "worktree", "remove", "--force", work)
		if removeErr == nil {
			_, removeErr = r.git(ctx, r.Root, "branch", "-D", branch)
		}
		return "", "", errors.Join(err, removeErr)
	}
	return work, branch, nil
}

func (r *Runner) commit(ctx context.Context, work, message string, always bool) error {
	if _, err := r.git(ctx, work, "add", "-A", "--", "."); err != nil {
		return err
	}
	status, err := r.git(ctx, work, "diff", "--cached", "--name-only")
	if err != nil {
		return err
	}
	if status == "" && !always {
		return nil
	}
	_, err = r.git(ctx, work, "commit", "--allow-empty", "-m", message)
	return err
}
