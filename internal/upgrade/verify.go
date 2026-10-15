package upgrade

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/exec"
)

func phaseGoFix(ctx context.Context, s *Step) (bool, error) {
	hasAPI, err := phaseHasAPI(s)
	if err != nil || !hasAPI {
		return false, err
	}
	if _, err := phaseGo(ctx, s, "fix", "./..."); err != nil {
		return false, err
	}
	result, err := s.Runner.Run(ctx, exec.Cmd{Name: "git", Args: []string{"status", "--porcelain", "--", "api/"}, Dir: s.Worktree})
	if err != nil {
		return false, fmt.Errorf("inspect go fix changes with git status --porcelain: %w\nTry: check Git in the upgrade worktree and run spinup upgrade --continue", err)
	}
	return len(result.Stdout) != 0, nil
}

func (h *phaseHooks) verify(ctx context.Context, s *Step) error {
	owners := h.provenance(s)
	var failures []string
	hasAPI, err := phaseHasAPI(s)
	if err != nil {
		failures = append(failures, err.Error())
	} else if hasAPI {
		for _, command := range []string{"build", "vet", "test"} {
			result, err := phaseGo(ctx, s, command, "./...")
			if err != nil {
				message := fmt.Sprintf("go %s ./...: %v\n%s%s", command, err, result.Stdout, result.Stderr)
				failures = append(failures, attributeFailure(message, owners, s.Worktree))
			}
		}
	}
	for _, report := range h.doctor(ctx, doctor.Local, &doctor.Context{
		Root: s.Worktree,
		Deps: map[string]any{"runner": s.Runner, "cli_version": s.Lock.CLIVersion},
	}) {
		if report.Result.Status == doctor.Fail {
			failures = append(failures, attributeFailure("doctor "+report.Check.ID+": "+report.Result.Message, owners, s.Worktree))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("upgrade verification failed:\n%s\nTry: fix the failures in %s, then run spinup upgrade --continue", strings.Join(failures, "\n"), s.Worktree)
}

func attributeFailure(message string, owners map[string]string, worktree string) string {
	paths := make([]string, 0, len(owners))
	for path := range owners {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	// Commands run in api/. Match Go diagnostics relative to that directory too.
	normalized := filepath.ToSlash(message)
	for _, path := range paths {
		relative := strings.TrimPrefix(path, "api/")
		absolute := filepath.ToSlash(filepath.Join(worktree, filepath.FromSlash(path)))
		if strings.Contains(normalized, path) || strings.Contains(normalized, relative) || strings.Contains(normalized, absolute) {
			message += fmt.Sprintf("\n  %s: last changed by migration %s", path, owners[path])
		}
	}
	return message
}
