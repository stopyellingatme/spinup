package upgrade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stopyellingatme/spinup/internal/compat"
	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/merge"
)

// CheckGitVersion is the version-check adapter until the merge implementation is installed.
var CheckGitVersion = checkGitVersion

// NewMerger constructs the Git merger until merge.NewGit is installed.
var NewMerger = func(r exec.Runner) merge.Merger { return gitMerger{runner: r} }

func checkGitVersion(ctx context.Context, runner exec.Runner) error {
	result, err := runner.Run(ctx, exec.Cmd{Name: "git", Args: []string{"--version"}})
	if err != nil {
		return fmt.Errorf("check Git version: %w\nTry: install Git 2.44.0 or later", err)
	}
	fields := strings.Fields(string(result.Stdout))
	if len(fields) < 3 {
		return fmt.Errorf("check Git version: invalid version output\nTry: install Git 2.44.0 or later")
	}
	parts := strings.Split(fields[2], ".")
	if len(parts) > 3 {
		parts = parts[:3]
	}
	version, err := compat.ParseVersion(strings.Join(parts, "."))
	if err != nil || version.Compare(compat.Version{Major: 2, Minor: 44}) < 0 {
		return fmt.Errorf("check Git version: Git 2.44.0 or later is required\nTry: update Git")
	}
	return nil
}

type gitMerger struct{ runner exec.Runner }

func (m gitMerger) Merge(ctx context.Context, in merge.Input) (merge.Result, error) {
	dir, err := os.MkdirTemp("", "spinup-merge-")
	if err != nil {
		return merge.Result{}, err
	}
	defer os.RemoveAll(dir)
	names := []string{"ours", "base", "theirs"}
	for i, data := range [][]byte{in.Ours, in.Base, in.Theirs} {
		if err := os.WriteFile(filepath.Join(dir, names[i]), data, 0600); err != nil {
			return merge.Result{}, err
		}
	}
	result, err := m.runner.Run(ctx, exec.Cmd{Name: "git", Dir: dir, Args: []string{"merge-file", "-p", "--diff3", "--diff-algorithm=histogram", "-L", "ours: " + in.Path, "-L", "base: " + in.Path, "-L", "theirs: " + in.Path, "ours", "base", "theirs"}})
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || result.ExitCode < 1 || result.ExitCode > 127 {
			return merge.Result{}, err
		}
	}
	return merge.Result{Data: result.Stdout, Conflicted: result.ExitCode != 0}, nil
}

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
		return fmt.Errorf("check upgrade storage: .spinup/ must be ignored by Git\nTry: add .spinup/ to the app .gitignore and commit it")
	}
	tracked, err := r.git(ctx, dir, "ls-files", "--", ".spinup")
	if err != nil {
		return err
	}
	if strings.TrimSpace(tracked) != "" {
		return fmt.Errorf("check upgrade storage: .spinup/ contains tracked files\nTry: remove .spinup/ from the Git index and commit the change")
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
