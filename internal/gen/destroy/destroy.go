// Package destroy removes unchanged generator files and preserves migrations.
package destroy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/fields"
	_ "github.com/stopyellingatme/spinup/internal/gen/handler" // Register the handler planner.
	_ "github.com/stopyellingatme/spinup/internal/gen/openapi" // Register the bundle post-step.
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/gen/resource"
	genrun "github.com/stopyellingatme/spinup/internal/gen/run"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// Options identifies the generated files and controls the preview.
type Options struct {
	// Root is the project directory.
	Root string
	// Kind is resource, model, or handler.
	Kind string
	// Name identifies the generated item.
	Name string
	// DryRun previews deletions without writing or running tools.
	DryRun bool
	// Out receives the file actions and migration suggestion.
	Out io.Writer
}

// Plan describes safe deletions, retained files, and the suggested migration.
type Plan struct {
	// Files contains only safe deletions.
	Files vfs.Plan
	// Kept lists changed, untracked, or linked files.
	Kept []string
	// Next suggests a migration without deleting an existing migration.
	Next string
	// Dirs lists the directories that the deletions leave empty, deepest first.
	Dirs []string
}

// Build compares generator paths with the lock without changing the project.
func Build(ctx context.Context, o Options, lock *manifest.Lock) (*Plan, error) {
	var paths []string
	result := &Plan{}
	switch o.Kind {
	case "resource", "model":
		name, err := fields.ParseName(o.Name)
		if err != nil {
			return nil, err
		}
		paths = resource.Paths(name)
		if o.Kind == "model" {
			paths = []string{resource.QueriesPath(name), resource.SchemasPath(name)}
		}
		result.Next = "spinup db new drop_" + name.PluralSnake()
	case "handler":
		g, _ := registry.Lookup("handler")
		output, err := g.Run(ctx, gen.Input{Root: o.Root, Lock: lock, Args: []string{o.Name}})
		if err != nil {
			return nil, err
		}
		paths = output.Plan.Paths()
	default:
		return nil, fmt.Errorf("cannot destroy %q: unsupported kind\nTry: spinup destroy resource|model|handler <name>", o.Kind)
	}
	for _, path := range paths {
		clean, err := vfs.CleanPath(path)
		if err != nil {
			return nil, err
		}
		if clean != path || strings.HasPrefix(path, "api/db/migrations/") {
			return nil, destroyError(fmt.Errorf("unsafe generator path %q", path))
		}
		disk := filepath.Join(o.Root, filepath.FromSlash(path))
		// Reject links in every path component, including directory links.
		linked, err := hasLink(o.Root, path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, destroyError(err)
		}
		if linked {
			result.Kept = append(result.Kept, path)
			continue
		}
		data, err := os.ReadFile(disk)
		if err != nil {
			return nil, destroyError(err)
		}
		entry, tracked := lock.Files[path]
		if !tracked || entry.Hash != manifest.Hash(data) {
			result.Kept = append(result.Kept, path)
			continue
		}
		if err := result.Files.Add(vfs.File{Path: path, Delete: true, Class: string(entry.Class)}); err != nil {
			return nil, err
		}
	}
	if o.Kind == "resource" {
		navPlan, err := resource.PlanNav(o.Root, result.Files, result.Files.Paths())
		if err != nil {
			return nil, err
		}
		result.Files = navPlan
	}
	result.Files = result.Files.Sorted()
	var deleted []string
	for _, file := range result.Files.Files {
		if file.Delete {
			deleted = append(deleted, file.Path)
		}
	}
	dirs, err := emptiedDirs(o.Root, deleted)
	if err != nil {
		return nil, err
	}
	result.Dirs = dirs
	return result, nil
}

func hasLink(root, path string) (bool, error) {
	current := root
	for _, part := range strings.Split(path, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return true, nil
		}
	}
	return false, nil
}

// Run previews or applies safe deletions, saves the lock, and runs registered post-steps.
func Run(ctx context.Context, o Options, runner exec.Runner) error {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if runner == nil {
		runner = exec.OSRunner{}
	}
	lockPath := filepath.Join(o.Root, "spinup.lock")
	lock, err := manifest.Load(lockPath)
	if err != nil {
		return err
	}
	plan, err := Build(ctx, o, lock)
	if err != nil {
		return err
	}
	for _, path := range plan.Kept {
		if _, err := fmt.Fprintf(o.Out, "keep %s (changed or untracked)\nTry: delete it by hand, or keep it\n", path); err != nil {
			return destroyError(err)
		}
	}
	for _, file := range plan.Files.Files {
		action := "remove"
		if !file.Delete {
			action = "update"
		}
		if o.DryRun {
			action = "would " + action
		}
		if _, err := fmt.Fprintf(o.Out, "%s %s\n", action, file.Path); err != nil {
			return destroyError(err)
		}
	}
	for _, dir := range plan.Dirs {
		action := "remove"
		if o.DryRun {
			action = "would remove"
		}
		if _, err := fmt.Fprintf(o.Out, "%s %s/\n", action, dir); err != nil {
			return destroyError(err)
		}
	}
	if !o.DryRun && len(plan.Files.Files) > 0 {
		// Recheck immediately before apply so a stale preview never authorizes deletion.
		checked, err := Build(ctx, o, lock)
		if err != nil {
			return err
		}
		if !slices.Equal(checked.Files.Paths(), plan.Files.Paths()) {
			return destroyError(errors.New("files changed during destruction"))
		}
		if _, err := vfs.Apply(o.Root, plan.Files, vfs.ApplyOptions{}); err != nil {
			return err
		}
		if err := removeDirs(o.Root, plan.Dirs); err != nil {
			return err
		}
		for _, file := range plan.Files.Files {
			if file.Delete {
				delete(lock.Files, file.Path)
				continue
			}
			mode := file.Mode.Perm()
			if mode == 0 {
				mode = 0o644
			}
			if err := lock.SetFile(file.Path, manifest.FileEntry{Class: manifest.Class(file.Class), Template: "gen/resource", Hash: manifest.Hash(file.Data), Mode: fmt.Sprintf("%04o", mode)}); err != nil {
				return err
			}
		}
		if err := lock.Save(lockPath); err != nil {
			return err
		}
		if err := postSteps(ctx, o, lock, runner); err != nil {
			return err
		}
	}
	if plan.Next != "" {
		if _, err := fmt.Fprintln(o.Out, "Next: "+plan.Next); err != nil {
			return destroyError(err)
		}
	}
	return nil
}

func postSteps(ctx context.Context, o Options, lock *manifest.Lock, runner exec.Runner) error {
	// The existing runner is the public entry point for the post-step registry.
	// An unchanged plan requests formatting without changing lock ownership.
	var plan vfs.Plan
	for path, entry := range lock.Files {
		if !strings.HasSuffix(path, ".go") || entry.Class != manifest.Managed || !strings.HasPrefix(entry.Template, "gen/") {
			continue
		}
		linked, err := hasLink(o.Root, path)
		if err != nil {
			return destroyError(err)
		}
		if linked {
			continue
		}
		data, err := os.ReadFile(filepath.Join(o.Root, filepath.FromSlash(path)))
		if err != nil {
			return destroyError(err)
		}
		if manifest.Hash(data) != entry.Hash {
			continue
		}
		if err := plan.Add(vfs.File{Path: path, Data: data, Class: string(entry.Class)}); err != nil {
			return err
		}
	}
	steps := []gen.PostStep{gen.PostOpenAPI, gen.PostSQLC, gen.PostGofmt}
	queries, err := filepath.Glob(filepath.Join(o.Root, "api/db/queries/*.sql"))
	if err != nil {
		return destroyError(err)
	}
	if len(queries) == 0 {
		// Match db sqlc: sqlc cannot generate a package without any queries.
		steps = []gen.PostStep{gen.PostOpenAPI, gen.PostGofmt}
		if _, err := fmt.Fprintln(o.Out, "No queries remain; sqlc skipped."); err != nil {
			return destroyError(err)
		}
	}
	g := gen.Generator{Name: "destroy", Run: func(context.Context, gen.Input) (*gen.Output, error) {
		return &gen.Output{Plan: plan.Sorted(), PostSteps: steps}, nil
	}}
	_, err = genrun.Run(ctx, g, nil, nil, genrun.Options{Root: o.Root, Runner: runner})
	return err
}

func destroyError(err error) error {
	return fmt.Errorf("cannot destroy generated files: %w\nTry: check the project files and permissions, then repeat spinup destroy", err)
}
