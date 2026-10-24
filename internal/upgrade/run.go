package upgrade

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/huh/v2"

	"github.com/stopyellingatme/spinup/internal/compat"
	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/merge"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// Runner coordinates upgrade planning, worktrees, phases, and recovery.
type Runner struct {
	// Root is the main checkout of the application.
	Root string
	// Source loads the old and new template versions.
	Source templates.Source
	// Merger merges edited text files.
	Merger merge.Merger
	// Exec runs Git and all phase subprocesses.
	Exec exec.Runner
	// Out and ErrOut receive progress and diagnostic output.
	Out, ErrOut io.Writer
	// In supplies terminal confirmation input.
	In io.Reader
	// Interactive permits terminal confirmation prompts.
	Interactive bool
	// CLIVersion is checked against the minimum version in the lock.
	CLIVersion string
}

func (r *Runner) defaults() error {
	if r.Root == "" {
		r.Root = "."
	}
	root, err := filepath.Abs(r.Root)
	if err != nil {
		return fmt.Errorf("resolve upgrade root: %w\nTry: select a project with --dir", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve upgrade root: %w\nTry: select an existing project with --dir", err)
	}
	r.Root = root
	if r.Exec == nil {
		r.Exec = exec.OSRunner{}
	}
	if r.Out == nil {
		r.Out = io.Discard
	}
	if r.ErrOut == nil {
		r.ErrOut = io.Discard
	}
	if r.In == nil {
		r.In = strings.NewReader("")
	}
	if r.CLIVersion == "" {
		r.CLIVersion = "dev"
	}
	if r.Merger == nil {
		r.Merger = NewMerger(r.Exec)
	}
	return nil
}

func (r *Runner) lock() (*manifest.Lock, error) {
	lock, err := manifest.Load(filepath.Join(r.Root, "spinup.lock"))
	if err != nil {
		return nil, err
	}
	if err := compat.CheckMinCLI(r.CLIVersion, lock.MinCLI); err != nil {
		return nil, err
	}
	return lock, nil
}

func (r *Runner) confirm(ctx context.Context, o Options, action string) error {
	if o.Yes {
		return nil
	}
	if !r.Interactive {
		return fmt.Errorf("cannot %s: confirmation requires a terminal\nTry: pass --yes", action)
	}
	accepted := false
	field := huh.NewConfirm().Title(action + "?").Value(&accepted)
	if err := huh.NewForm(huh.NewGroup(field)).WithInput(r.In).WithOutput(r.Out).RunWithContext(ctx); err != nil {
		return fmt.Errorf("read upgrade confirmation: %w\nTry: use a terminal or pass --yes", err)
	}
	if !accepted {
		return fmt.Errorf("cannot %s: confirmation declined\nTry: pass --yes when ready", action)
	}
	return nil
}

// Run applies the version chain in an isolated branch and worktree.
func (r *Runner) Run(ctx context.Context, o Options) error {
	if o.DryRun {
		_, err := r.Plan(ctx, o)
		return err
	}
	if err := r.defaults(); err != nil {
		return err
	}
	if _, err := os.Stat(statePath(r.Root)); err == nil {
		return fmt.Errorf("cannot start upgrade: an upgrade is in progress, use --continue or --abort\nTry: spinup upgrade --continue or spinup upgrade --abort")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect upgrade state: %w\nTry: check .spinup/ permissions", err)
	}
	if err := r.checkRepository(ctx); err != nil {
		return err
	}
	status, err := r.git(ctx, r.Root, "status", "--porcelain")
	if err != nil {
		return err
	}
	if status != "" && !o.AllowDirty {
		return fmt.Errorf("cannot upgrade: the Git tree has uncommitted changes\nTry: commit the changes or pass --allow-dirty")
	}
	lock, err := r.lock()
	if err != nil {
		return err
	}
	if err := CheckGitVersion(ctx, r.Exec); err != nil {
		return err
	}
	versions, err := chain(ctx, r.Source, lock.TemplateVersion, o.To, nil)
	if err != nil {
		return err
	}
	versions, err = r.pluginChain(ctx, lock, versions, o.Offline)
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		_, err = fmt.Fprintln(r.Out, "Already up to date.")
		return err
	}
	if _, err = fmt.Fprintf(r.Out, "Upgrade %s -> %s (%d steps)\n", lock.TemplateVersion, versions[len(versions)-1], len(versions)); err != nil {
		return err
	}
	// Preview every step before confirmation. The preview writes only temporary files.
	if _, err := r.preview(ctx, lock, versions, o); err != nil {
		return err
	}
	if err := r.confirm(ctx, o, "Apply upgrade"); err != nil {
		return err
	}
	work, branch, err := r.createWorktree(ctx, versions[len(versions)-1])
	if err != nil {
		return err
	}
	state := &runState{Schema: 1, Chain: versions, Worktree: work, Branch: branch, Status: "step", Lock: lock, Options: o, UI: uiEnabled.Load()}
	if err := saveState(r.Root, state); err != nil {
		return err
	}
	return r.resume(ctx, state)
}

// Continue checks conflict resolutions and resumes the recorded upgrade.
func (r *Runner) Continue(ctx context.Context, o Options) error {
	if err := r.defaults(); err != nil {
		return err
	}
	state, err := loadState(r.Root)
	if err != nil {
		return err
	}
	if err := r.checkRepository(ctx); err != nil {
		return err
	}
	if err := r.checkWorktree(ctx, state); err != nil {
		return err
	}
	if state.Status == "conflicts" || state.Status == "after-conflicts" {
		paths, err := unresolved(state.Worktree, state.Conflicts)
		if err != nil {
			return err
		}
		if len(paths) > 0 {
			return conflictError(state.Worktree, paths)
		}
	}
	if err := r.confirm(ctx, o, "Continue upgrade"); err != nil {
		return err
	}
	// Preserve the original policy when the user supplies no additional flags.
	state.Options.NoVerify = state.Options.NoVerify || o.NoVerify
	state.Options.CommitConflicts = state.Options.CommitConflicts || o.CommitConflicts
	state.UI = state.UI || uiEnabled.Load()
	if state.Status == "conflicts" || state.Status == "after-conflicts" {
		if err := state.resolvePlanConflicts(); err != nil {
			return err
		}
	}
	if state.Status == "conflicts" {
		if err := r.commit(ctx, state.Worktree, "spinup upgrade "+state.Plan.From+" -> "+state.Plan.To, true); err != nil {
			return err
		}
		state.Conflicts = nil
		state.Status = "gofix"
	}
	if state.Status == "after-conflicts" {
		// The lock commit includes the resolved files.
		state.Conflicts = nil
		state.Status = "lock"
	}
	if err := saveState(r.Root, state); err != nil {
		return err
	}
	if state.Status == "step" {
		// Discard the interrupted attempt before planning from the last commit.
		if _, err := r.git(ctx, state.Worktree, "reset", "--hard", "HEAD"); err != nil {
			return err
		}
		if _, err := r.git(ctx, state.Worktree, "clean", "-fd", "-e", ".spinup"); err != nil {
			return err
		}
	}
	return r.resume(ctx, state)
}

// Abort removes the recorded upgrade worktree and branch after confirmation.
func (r *Runner) Abort(ctx context.Context, o Options) error {
	if err := r.defaults(); err != nil {
		return err
	}
	state, err := loadState(r.Root)
	if err != nil {
		return err
	}
	if err := r.checkRepository(ctx); err != nil {
		return err
	}
	if err := r.confirm(ctx, o, "Abort upgrade and remove its worktree"); err != nil {
		return err
	}
	if _, err := os.Stat(state.Worktree); err == nil {
		if err := r.checkWorktree(ctx, state); err != nil {
			return err
		}
		if _, err := r.git(ctx, r.Root, "worktree", "remove", "--force", state.Worktree); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect upgrade worktree: %w\nTry: check the worktree permissions", err)
	} else if _, err := r.git(ctx, r.Root, "worktree", "prune"); err != nil {
		return err
	}
	branches, err := r.git(ctx, r.Root, "branch", "--list", state.Branch)
	if err != nil {
		return err
	}
	if strings.TrimSpace(branches) != "" {
		if _, err := r.git(ctx, r.Root, "branch", "-D", state.Branch); err != nil {
			return err
		}
	}
	return r.clearState()
}

func (r *Runner) checkWorktree(ctx context.Context, state *runState) error {
	top, err := r.git(ctx, state.Worktree, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if strings.TrimSpace(top) != state.Worktree {
		return fmt.Errorf("resume upgrade: the worktree path does not match its Git root\nTry: restore the upgrade worktree at %s", state.Worktree)
	}
	branch, err := r.git(ctx, state.Worktree, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(branch) != state.Branch {
		return fmt.Errorf("resume upgrade: worktree is on an unexpected branch\nTry: restore branch %s in %s", state.Branch, state.Worktree)
	}
	if err := r.checkIgnored(ctx, state.Worktree); err != nil {
		return err
	}
	return nil
}

func (r *Runner) clearState() error {
	if err := os.Remove(statePath(r.Root)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove upgrade state: %w\nTry: check .spinup/ permissions", err)
	}
	return nil
}

func (r *Runner) step(ctx context.Context, state *runState) (*Step, error) {
	if r.Source == nil {
		return nil, fmt.Errorf("load upgrade step: template source is nil\nTry: configure the template source")
	}
	to, err := r.Source.Load(ctx, state.Chain[state.Index])
	if err != nil {
		return nil, err
	}
	fromVersion := state.Lock.TemplateVersion
	if state.Plan != nil {
		fromVersion = state.Plan.From
	}
	from, err := r.Source.Load(ctx, fromVersion)
	if err != nil && !errors.Is(err, templates.ErrVersionUnavailable) {
		return nil, err
	}
	answers := clonePlanAnswers(state.Lock.Answers)
	if state.Module != nil && state.Status == "step" {
		answers = clonePlanAnswers(state.Module.Answers)
	}
	return &Step{From: from, To: to, Worktree: state.Worktree, Lock: state.Lock, Answers: answers, Plan: state.Plan, Options: state.Options, Runner: r.Exec, Out: r.Out, Merger: r.Merger}, nil
}

func (r *Runner) resume(ctx context.Context, state *runState) error {
	// Extensions use the recorded choice, including after a process restart.
	ctx = context.WithValue(ctx, uiChoiceKey{}, state.UI)
	phases := CurrentPhases()
	for state.Index < len(state.Chain) {
		step, err := r.step(ctx, state)
		if err != nil {
			return err
		}
		if state.Status == "step" {
			oldAnswers := clonePlanAnswers(state.Lock.Answers)
			targetPacks := PackSet{}
			if state.Module != nil {
				targetPacks.Names = state.Module.Packs
			}
			if err := callPhase(ctx, phases.Before, step); err != nil {
				return err
			}
			oldPacks, newPacks, err := r.pluginPacks(ctx, step.Lock, step.From, step.To, targetPacks, step.Options.Offline)
			if err != nil {
				return err
			}
			step.Plan, err = BuildPlan(ctx, PlanInput{Root: step.Worktree, Lock: step.Lock, Old: step.From, New: step.To, OldAnswers: oldAnswers, NewAnswers: step.Answers, OldPacks: &oldPacks, NewPacks: &newPacks, Merger: r.Merger, ForceManaged: step.Options.ForceManaged})
			if err != nil {
				return err
			}
			if err := requireBase(step.Plan, step.From, step.Plan.From); err != nil {
				return err
			}
			if state.Module != nil {
				if err := state.Module.editPlan(step.Worktree, step.Plan); err != nil {
					return err
				}
			}
			if err := step.Plan.Format(r.Out); err != nil {
				return err
			}
			step.Lock, err = NextLock(step.Lock, step.Plan, step.To, step.Answers, newPacks)
			if err != nil {
				return err
			}
			step.Lock.CLIVersion = r.CLIVersion
			if err := writePlan(step.Worktree, step.Plan); err != nil {
				return err
			}
			if err := callPhase(ctx, phases.Apply, step); err != nil {
				return err
			}
			if err := callPhase(ctx, phases.Deps, step); err != nil {
				return err
			}
			state.Plan, state.Lock = step.Plan, step.Lock
			state.Conflicts = step.Plan.Conflicts()
			if len(state.Conflicts) > 0 && !step.Options.CommitConflicts {
				state.Status = "conflicts"
				if err := saveState(r.Root, state); err != nil {
					return err
				}
				return conflictError(state.Worktree, state.Conflicts)
			}
			if err := r.commit(ctx, step.Worktree, "spinup upgrade "+step.Plan.From+" -> "+step.Plan.To, true); err != nil {
				return err
			}
			state.CommittedConflicts = append(state.CommittedConflicts, state.Conflicts...)
			slices.Sort(state.CommittedConflicts)
			state.CommittedConflicts = slices.Compact(state.CommittedConflicts)
			state.Status = "gofix"
			if err := saveState(r.Root, state); err != nil {
				return err
			}
		}
		if state.Status == "gofix" {
			if phases.GoFix != nil {
				changed, err := phases.GoFix(ctx, step)
				if err != nil {
					return phaseError("GoFix", err)
				}
				if changed {
					if err := r.commit(ctx, step.Worktree, "spinup upgrade: go fix", false); err != nil {
						return err
					}
				}
			}
			state.Status = "after"
			state.Lock = step.Lock
			if err := saveState(r.Root, state); err != nil {
				return err
			}
		}
		if state.Status == "after" {
			if err := callPhase(ctx, phases.After, step); err != nil {
				return err
			}
			for _, extension := range Extensions() {
				if extension.Run != nil {
					if err := extension.Run(ctx, step); err != nil {
						return phaseError(extension.Name, err)
					}
				}
			}
			state.Lock = step.Lock
			if len(step.Conflicts) > 0 && !step.Options.CommitConflicts {
				// Markers added after planning (by an extension) stop the step like plan
				// conflicts; --continue resumes at the lock commit, which includes them.
				state.Conflicts = slices.Compact(slices.Sorted(slices.Values(step.Conflicts)))
				state.Status = "after-conflicts"
				if err := saveState(r.Root, state); err != nil {
					return err
				}
				return conflictError(state.Worktree, state.Conflicts)
			}
			state.CommittedConflicts = append(state.CommittedConflicts, step.Conflicts...)
			slices.Sort(state.CommittedConflicts)
			state.CommittedConflicts = slices.Compact(state.CommittedConflicts)
			state.Status = "lock"
			if err := saveState(r.Root, state); err != nil {
				return err
			}
		}
		if state.Status == "lock" {
			if err := step.Lock.Save(filepath.Join(step.Worktree, "spinup.lock")); err != nil {
				return err
			}
			if err := r.commit(ctx, step.Worktree, "spinup upgrade: lock "+step.To.Version, true); err != nil {
				return err
			}
			state.Status = "verify"
			if err := saveState(r.Root, state); err != nil {
				return err
			}
		}
		if state.Status == "verify" && !step.Options.NoVerify {
			if err := callPhase(ctx, phases.Verify, step); err != nil {
				return err
			}
		}
		state.Index++
		if state.Index < len(state.Chain) {
			state.Status = "step"
			state.Plan = nil
			state.Conflicts = nil
			if err := saveState(r.Root, state); err != nil {
				return err
			}
		}
	}
	if err := r.clearState(); err != nil {
		return err
	}
	if len(state.CommittedConflicts) > 0 {
		_, err := fmt.Fprintf(r.Out, "Warning: committed conflict markers in: %s\nResolve these files before running git merge %s.\nWorktree: %s\n", strings.Join(state.CommittedConflicts, ", "), state.Branch, state.Worktree)
		return err
	}
	_, err := fmt.Fprintf(r.Out, "Next: git merge %s\nWorktree: %s\nRemove it with: git worktree remove %s\n", state.Branch, state.Worktree, state.Worktree)
	return err
}

func callPhase(ctx context.Context, phase func(context.Context, *Step) error, step *Step) error {
	if phase == nil {
		return nil
	}
	if err := phase(ctx, step); err != nil {
		return phaseError("phase", err)
	}
	return nil
}
func phaseError(name string, err error) error {
	// Keep the Try line of the inner error, so the message has one Try line.
	if strings.Contains(err.Error(), "\nTry: ") {
		return fmt.Errorf("upgrade %s failed: %w", name, err)
	}
	return fmt.Errorf("upgrade %s failed: %w\nTry: correct the failure, then run spinup upgrade --continue or --abort", name, err)
}

func writePlan(work string, plan *Plan) error {
	root, err := os.OpenRoot(work)
	if err != nil {
		return fmt.Errorf("open upgrade worktree: %w\nTry: restore the upgrade worktree", err)
	}
	defer root.Close()
	for _, file := range plan.Files {
		clean, err := vfs.CleanPath(file.Path)
		if err != nil || clean != file.Path || strings.Split(clean, "/")[0] == ".git" || strings.Split(clean, "/")[0] == ".spinup" {
			return fmt.Errorf("write upgrade file %q: unsafe destination\nTry: correct the template path", file.Path)
		}
		switch file.Action {
		case ActionDelete:
			err = root.Remove(file.Path)
		case ActionOverwrite, ActionMerge, ActionConflict, ActionCreate, ActionRename:
			err = root.MkdirAll(filepath.Dir(file.Path), 0755)
			if err == nil {
				err = root.WriteFile(file.Path, file.Data, file.Mode.Perm())
			}
			if err == nil {
				err = root.Chmod(file.Path, file.Mode.Perm())
			}
			if err == nil && file.From != "" {
				from, cleanErr := vfs.CleanPath(file.From)
				if cleanErr != nil || from != file.From || strings.Split(from, "/")[0] == ".git" || strings.Split(from, "/")[0] == ".spinup" {
					return fmt.Errorf("remove rename source %q: unsafe path\nTry: correct the rename source", file.From)
				}
				err = root.Remove(file.From)
			}
		case ActionKeep, ActionOrphan, ActionUnchanged:
			if file.Mode.Perm() != 0 {
				info, statErr := root.Stat(file.Path)
				switch {
				case errors.Is(statErr, os.ErrNotExist) && file.Action == ActionKeep:
				case statErr != nil:
					err = statErr
				case info.Mode().Perm() != file.Mode.Perm():
					err = root.Chmod(file.Path, file.Mode.Perm())
				}
			}
		default:
			return fmt.Errorf("write upgrade file %q: unsupported action %q\nTry: rebuild the upgrade plan", file.Path, file.Action)
		}
		if err != nil {
			return fmt.Errorf("apply upgrade file %q: %w\nTry: correct the file permissions and continue the upgrade", file.Path, err)
		}
	}
	return nil
}
