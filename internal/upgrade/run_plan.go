package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/templates"
)

// PlanReport contains the plans for the complete version chain.
// Its JSON representation excludes file contents.
type PlanReport struct {
	// Steps lists each plan in upgrade order.
	Steps []*Plan
}

type reportFile struct {
	Path     string         `json:"path"`
	From     string         `json:"from,omitempty"`
	Action   Action         `json:"action"`
	Class    manifest.Class `json:"class"`
	Template string         `json:"template,omitempty"`
	Mode     string         `json:"mode"`
	Reason   string         `json:"reason,omitempty"`
}
type reportStep struct {
	From           string       `json:"from"`
	To             string       `json:"to"`
	Files          []reportFile `json:"files"`
	Tombstoned     []string     `json:"tombstoned"`
	MissingManaged []string     `json:"missing_managed"`
}

// MarshalJSON encodes stable plan metadata without template or application contents.
func (p PlanReport) MarshalJSON() ([]byte, error) {
	steps := make([]reportStep, 0, len(p.Steps))
	for _, plan := range p.Steps {
		step := reportStep{From: plan.From, To: plan.To, Files: []reportFile{}, Tombstoned: plan.Tombstoned, MissingManaged: plan.MissingManaged}
		for _, f := range plan.Files {
			step.Files = append(step.Files, reportFile{Path: f.Path, From: f.From, Action: f.Action, Class: f.Class, Template: f.Template, Mode: fmt.Sprintf("%04o", f.Mode.Perm()), Reason: f.Reason})
		}
		steps = append(steps, step)
	}
	return json.Marshal(struct {
		Steps []reportStep `json:"steps"`
	}{Steps: steps})
}

// Plan previews every step and writes only .spinup/plan.json in the project.
func (r *Runner) Plan(ctx context.Context, o Options) (*PlanReport, error) {
	if err := r.defaults(); err != nil {
		return nil, err
	}
	lock, err := r.lock()
	if err != nil {
		return nil, err
	}
	versions, err := chain(ctx, r.Source, lock.TemplateVersion, o.To, nil)
	if err != nil {
		return nil, err
	}
	report, err := r.preview(ctx, lock, versions, o)
	if err != nil {
		return nil, err
	}
	if err := writeUpgradeJSON(filepath.Join(r.Root, ".spinup", "plan.json"), report); err != nil {
		return nil, err
	}
	return report, nil
}

func (r *Runner) preview(ctx context.Context, lock *manifest.Lock, versions []string, o Options, changes ...*moduleChange) (*PlanReport, error) {
	var change *moduleChange
	targetPacks := PackSet{}
	answers := lock.Answers
	if len(changes) > 0 && changes[0] != nil {
		change = changes[0]
		targetPacks.Names, answers = change.Packs, change.Answers
	}
	_, namesForRender := targetPacks.resolve(nil, lock.Packs)
	temp, err := os.MkdirTemp("", "spinup-plan-")
	if err != nil {
		return nil, fmt.Errorf("create plan directory: %w\nTry: check temporary directory permissions", err)
	}
	defer os.RemoveAll(temp)
	paths := make(map[string]bool)
	for name := range lock.Files {
		paths[name] = true
	}
	for _, version := range versions {
		to, loadErr := r.Source.Load(ctx, version)
		if loadErr != nil {
			return nil, loadErr
		}
		rendered, renderErr := Render(to, namesForRender, answers)
		if renderErr != nil {
			return nil, renderErr
		}
		for _, file := range rendered.Files {
			paths[file.Path] = true
		}
	}
	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	slices.Sort(names)
	if err := copyPlanTree(ctx, r.Root, temp, names); err != nil {
		return nil, fmt.Errorf("copy project for planning: %w\nTry: check project file permissions", err)
	}
	report := &PlanReport{Steps: []*Plan{}}
	if len(versions) == 0 {
		if _, err := fmt.Fprintln(r.Out, "Already up to date."); err != nil {
			return nil, err
		}
	}
	for _, version := range versions {
		to, err := r.Source.Load(ctx, version)
		if err != nil {
			return nil, err
		}
		from, err := r.Source.Load(ctx, lock.TemplateVersion)
		if err != nil && !errors.Is(err, templates.ErrVersionUnavailable) {
			return nil, err
		}
		plan, err := BuildPlan(ctx, PlanInput{Root: temp, Lock: lock, Old: from, New: to, OldAnswers: lock.Answers, NewAnswers: answers, NewPacks: &targetPacks, Merger: r.Merger, ForceManaged: o.ForceManaged})
		if err != nil {
			return nil, err
		}
		if change != nil {
			if err := change.editPlan(temp, plan); err != nil {
				return nil, err
			}
		}
		if err := plan.Format(r.Out); err != nil {
			return nil, err
		}
		if err := printKitNotes(r.Out, to); err != nil {
			return nil, err
		}
		report.Steps = append(report.Steps, plan)
		if err := writePlan(temp, plan); err != nil {
			return nil, err
		}
		lock, err = NextLock(lock, plan, to, answers, targetPacks)
		if err != nil {
			return nil, err
		}
	}
	return report, nil
}

func printKitNotes(out io.Writer, to *templates.Version) error {
	if len(to.KitMigration) == 0 {
		return nil
	}
	var notes struct {
		Schema  int    `json:"schema"`
		Version string `json:"version"`
		Steps   []struct {
			ID   string `json:"id"`
			Desc string `json:"desc"`
			Kind string `json:"kind"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(to.KitMigration, &notes); err != nil {
		return fmt.Errorf("read kit notes for %s: %w\nTry: repair MIGRATION.json in the target templates", to.Version, err)
	}
	if notes.Schema != 1 {
		return fmt.Errorf("read kit notes for %s: unsupported schema\nTry: repair MIGRATION.json in the target templates", to.Version)
	}
	for _, step := range notes.Steps {
		if _, err := fmt.Fprintf(out, "Kit %s [%s]: %s\n", step.ID, step.Kind, step.Desc); err != nil {
			return fmt.Errorf("print kit notes: %w\nTry: use a writable output", err)
		}
	}
	return nil
}

// Copy only paths that planning reads. Unrelated files and ignored dependency trees
// can be large, contain secrets, or contain symlinks outside the project.
func copyPlanTree(ctx context.Context, source, target string, paths []string) error {
	root, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := root.Stat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		dest := filepath.Join(target, filepath.FromSlash(name))
		if info.IsDir() {
			if err := os.MkdirAll(dest, 0755); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("copy %q: not a regular file", name)
		}
		data, err := root.ReadFile(name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, data, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}
