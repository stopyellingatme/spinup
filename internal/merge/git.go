package merge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/stopyellingatme/spinup/internal/compat"
	"github.com/stopyellingatme/spinup/internal/exec"
)

// MinGitVersion is the oldest Git version that supports the required merge flags.
const MinGitVersion = "2.44.0"

var gitVersionPattern = regexp.MustCompile(`(?m)^git version ([0-9]+\.[0-9]+\.[0-9]+)(?:\.[^[:space:]]+)?(?:[[:space:]]|$)`)

type gitMerger struct {
	runner exec.Runner
}

// NewGit returns a Merger that uses git merge-file for each merge.
func NewGit(r exec.Runner) Merger {
	return gitMerger{runner: r}
}

func (m gitMerger) Merge(ctx context.Context, in Input) (Result, error) {
	if err := validateInput(in); err != nil {
		return Result{}, err
	}

	dir, err := os.MkdirTemp("", "spinup-merge-")
	if err != nil {
		return Result{}, fmt.Errorf("create merge directory: %w\nTry: check the temporary directory permissions", err)
	}
	defer os.RemoveAll(dir)

	ours := filepath.Join(dir, "ours")
	base := filepath.Join(dir, "base")
	theirs := filepath.Join(dir, "theirs")
	for _, file := range []struct {
		path string
		data []byte
	}{
		{path: ours, data: in.Ours},
		{path: base, data: in.Base},
		{path: theirs, data: in.Theirs},
	} {
		if err := os.WriteFile(file.path, file.data, 0o600); err != nil {
			return Result{}, fmt.Errorf("write merge input: %w\nTry: check the temporary directory permissions", err)
		}
	}

	result, runErr := m.runner.Run(ctx, exec.Cmd{
		Name: "git",
		Args: []string{
			"merge-file", "-p", "--diff3", "--diff-algorithm=histogram",
			"-L", in.Path + " (yours)",
			"-L", in.Path + " (base)",
			"-L", in.Path + " (new template)",
			ours, base, theirs,
		},
	})
	if result.ExitCode >= 1 && result.ExitCode <= 127 {
		return Result{Data: result.Stdout, Conflicted: true}, nil
	}
	if runErr != nil {
		return Result{}, fmt.Errorf("run git merge-file for %q: %w\nTry: install git %s or later and retry", in.Path, runErr, MinGitVersion)
	}
	if result.ExitCode != 0 {
		return Result{}, fmt.Errorf("git merge-file for %q returned exit code %d\nTry: inspect the git installation and retry", in.Path, result.ExitCode)
	}
	return Result{Data: result.Stdout}, nil
}

// CheckGitVersion verifies that git supports the merge-file options used by NewGit.
func CheckGitVersion(ctx context.Context, r exec.Runner) (string, error) {
	result, err := r.Run(ctx, exec.Cmd{Name: "git", Args: []string{"--version"}})
	if err != nil {
		return "", fmt.Errorf("check git version: %w\nTry: install git 2.44 or later", err)
	}

	match := gitVersionPattern.FindStringSubmatch(string(result.Stdout))
	if len(match) != 2 {
		return "", fmt.Errorf("cannot parse git version from %q\nTry: install git 2.44 or later", strings.TrimSpace(string(result.Stdout)))
	}
	found, err := compat.ParseVersion(match[1])
	if err != nil {
		return "", fmt.Errorf("cannot parse git version %q: %w\nTry: install git 2.44 or later", match[1], err)
	}
	minimum, err := compat.ParseVersion(MinGitVersion)
	if err != nil {
		return "", fmt.Errorf("parse minimum git version %q: %w\nTry: install git 2.44 or later", MinGitVersion, err)
	}
	if found.Compare(minimum) < 0 {
		return "", fmt.Errorf("git version check found %s, but requires %s\nTry: install git 2.44 or later", match[1], MinGitVersion)
	}
	return match[1], nil
}

func validateInput(in Input) error {
	for _, part := range []struct {
		name string
		data []byte
	}{
		{name: "base", data: in.Base},
		{name: "ours", data: in.Ours},
		{name: "theirs", data: in.Theirs},
	} {
		if len(part.data) > MaxSize {
			return fmt.Errorf("cannot merge %q: %s input is larger than %d bytes\nTry: overwrite or keep the file instead", in.Path, part.name, MaxSize)
		}
		if bytes.IndexByte(part.data, 0) >= 0 {
			return fmt.Errorf("cannot merge %q: %s input contains a NUL byte\nTry: overwrite or keep the binary file instead", in.Path, part.name)
		}
	}
	return nil
}
