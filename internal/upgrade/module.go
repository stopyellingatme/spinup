package upgrade

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/codemod"
	"github.com/stopyellingatme/spinup/internal/gen/newapp"
	"github.com/stopyellingatme/spinup/internal/modules"
	"github.com/stopyellingatme/spinup/internal/project"
)

// ModuleOptions contains requested modules, explicit question answers, and upgrade policy.
type ModuleOptions struct {
	// Names lists the requested modules.
	Names []string
	// Answers supplies explicit question values.
	Answers map[string]any
	// Prompter obtains missing answers in an interactive session.
	Prompter newapp.Prompter
	// Options controls confirmation and the upgrade phases.
	Options Options
}

// moduleChange persists the target inputs so recovery never needs another prompt.
type moduleChange struct {
	Names   []string       `json:"names"`
	Packs   []string       `json:"packs"`
	Added   []string       `json:"added"`
	Answers map[string]any `json:"answers"`
}

func (m *moduleChange) branch() string { return "spinup/module-" + strings.Join(m.Names, "-") }

func (m *moduleChange) valid() bool {
	if len(m.Names) == 0 || !slices.IsSorted(m.Names) || len(m.Packs) == 0 || m.Answers == nil {
		return false
	}
	for i, name := range m.Names {
		if _, ok := modules.Get(name); !ok || (i > 0 && m.Names[i-1] == name) {
			return false
		}
	}
	return true
}

// AddModules applies changed answers at the locked version through the upgrade state machine.
func (r *Runner) AddModules(ctx context.Context, o ModuleOptions) error {
	if err := r.defaults(); err != nil {
		return err
	}
	if _, err := os.Stat(statePath(r.Root)); err == nil {
		return fmt.Errorf("cannot add modules: an upgrade is in progress\nTry: spinup module add --continue or spinup module add --abort")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect upgrade state: %w\nTry: check .spinup/ permissions", err)
	}
	lock, err := r.lock()
	if err != nil {
		return err
	}
	cfg, err := project.Load(r.Root)
	if err != nil {
		return fmt.Errorf("load modules: %w\nTry: correct spinup.toml", err)
	}
	if r.Source == nil {
		return fmt.Errorf("add modules: template source is nil\nTry: configure the locked template source")
	}
	version, err := r.Source.Load(ctx, lock.TemplateVersion)
	if err != nil {
		return err
	}
	selection, err := modules.Select(ctx, modules.SelectOptions{Config: cfg, Lock: lock, FS: version.Packs, Names: o.Names, Answers: o.Answers, Interactive: r.Interactive, Prompter: o.Prompter})
	if err != nil {
		return err
	}
	configured := slices.Clone(cfg.Modules)
	slices.Sort(configured)
	if len(selection.Added) == 0 && slices.Equal(configured, selection.Answers["modules"].([]string)) {
		_, err := fmt.Fprintln(r.Out, "Modules are already enabled.")
		return err
	}
	if len(selection.Required) > 0 {
		if _, err := fmt.Fprintf(r.Out, "Adding required modules: %s\n", strings.Join(selection.Required, ", ")); err != nil {
			return err
		}
	}
	names := slices.Clone(o.Names)
	slices.Sort(names)
	names = slices.Compact(names)
	change := &moduleChange{Names: names, Packs: selection.Packs, Added: selection.Added, Answers: selection.Answers}
	o.Options.To = lock.TemplateVersion
	versions := []string{lock.TemplateVersion}
	// Planning uses the user's checkout, but application always starts from a clean HEAD.
	if !o.Options.DryRun {
		if err := r.checkRepository(ctx); err != nil {
			return err
		}
		status, err := r.git(ctx, r.Root, "status", "--porcelain")
		if err != nil {
			return err
		}
		if status != "" {
			return fmt.Errorf("cannot add modules: the Git tree has uncommitted changes\nTry: commit the changes before adding modules")
		}
		if err := CheckGitVersion(ctx, r.Exec); err != nil {
			return err
		}
	}
	if _, err := r.preview(ctx, lock, versions, o.Options, change); err != nil {
		return err
	}
	if o.Options.DryRun {
		return nil
	}
	if err := r.confirm(ctx, o.Options, "Add modules"); err != nil {
		return err
	}
	work := filepath.Join(r.Root, ".spinup", "upgrade", lock.TemplateVersion)
	if err := r.checkIgnored(ctx, r.Root); err != nil {
		return err
	}
	if _, err := r.git(ctx, r.Root, "worktree", "add", "-b", change.branch(), work, "HEAD"); err != nil {
		return err
	}
	if err := r.checkIgnored(ctx, work); err != nil {
		_, removeErr := r.git(ctx, r.Root, "worktree", "remove", "--force", work)
		if removeErr == nil {
			_, removeErr = r.git(ctx, r.Root, "branch", "-D", change.branch())
		}
		return errors.Join(err, removeErr)
	}
	state := &runState{Schema: 1, Chain: versions, Worktree: work, Branch: change.branch(), Status: "step", Lock: lock, Options: o.Options, Module: change, UI: uiEnabled.Load()}
	if err := saveState(r.Root, state); err != nil {
		return err
	}
	return r.resume(ctx, state)
}

// editPlan changes configuration through the TOML editor and preserves user comments.
// The locked version is unchanged, so only the module fields need a configuration edit.
func (m *moduleChange) editPlan(root string, plan *Plan) error {
	original, err := os.ReadFile(filepath.Join(root, "spinup.toml"))
	if err != nil {
		return fmt.Errorf("read module configuration: %w\nTry: restore spinup.toml", err)
	}
	enabled := []string{}
	for _, name := range m.Packs {
		if _, ok := modules.Get(name); ok {
			enabled = append(enabled, name)
		}
	}
	slices.Sort(enabled)
	data, err := codemod.SetTOML(original, nil, "modules", enabled)
	if err != nil {
		return err
	}
	// The base template declares this table even before monitoring is selected.
	if slices.Contains(m.Added, "monitoring") {
		if profile, ok := m.Answers["monitoring_profile"].(string); ok {
			data, err = codemod.SetTOML(data, []string{"monitoring"}, "profile", profile)
			if err != nil {
				return err
			}
		}
	}
	for i := range plan.Files {
		file := &plan.Files[i]
		if file.Path != "spinup.toml" {
			continue
		}
		file.Action, file.Data, file.Reason = ActionMerge, data, "module configuration"
		if bytes.Equal(original, data) {
			file.Action = ActionUnchanged
		}
		return nil
	}
	return fmt.Errorf("plan module configuration: spinup.toml is not tracked by the base pack\nTry: restore the base pack entry in spinup.lock")
}
