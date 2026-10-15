package ci

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/stopyellingatme/spinup/internal/gen"
	genrun "github.com/stopyellingatme/spinup/internal/gen/run"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/render"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// Generate renders the locked ci pack and applies it through the generator runner.
// Previews leave the files and lock unchanged. Force permits managed file conflicts.
func Generate(ctx context.Context, source templates.Source, o genrun.Options) (*genrun.Result, error) {
	if o.DryRun && o.Diff {
		return nil, fmt.Errorf("cannot generate CI: --dry-run and --diff are mutually exclusive\nTry: select one preview mode")
	}
	var rendered vfs.Plan
	generator := gen.Generator{Name: "ci", Run: func(ctx context.Context, in gen.Input) (*gen.Output, error) {
		if !slices.Contains(in.Lock.Packs, "ci") {
			return nil, fmt.Errorf("cannot generate CI: the lock does not enable the ci pack\nTry: use a project created with --modules ci")
		}
		if source == nil {
			return nil, fmt.Errorf("cannot generate CI: the template source is nil\nTry: supply the locked template version")
		}
		version, err := source.Load(ctx, in.Lock.TemplateVersion)
		if err != nil {
			return nil, err
		}
		if version == nil || version.Version != in.Lock.TemplateVersion {
			return nil, fmt.Errorf("cannot generate CI: the template source returned a different version\nTry: supply template version %s", in.Lock.TemplateVersion)
		}
		pack, err := templates.Load(version.Packs, "ci")
		if err != nil {
			return nil, err
		}
		rendered, err = render.Render(render.Input{Packs: []*templates.Pack{pack}, Answers: in.Lock.Answers, Stack: version.Stack, TemplateVersion: version.Version})
		if err != nil {
			return nil, err
		}
		changes, err := vfs.Diff(in.Root, rendered)
		if err != nil {
			return nil, err
		}
		output := &gen.Output{Plan: rendered}
		for _, change := range changes {
			entry, tracked := in.Lock.Files[change.Path]
			if !o.Force && change.Kind == vfs.Update && (!tracked || entry.Class != manifest.Managed || manifest.Hash(change.Old) != entry.Hash) {
				return nil, fmt.Errorf("cannot generate CI: %s has user changes\nTry: save the changes, then pass --force", change.Path)
			}
			// The preflight above checks disk hashes even for older gen/ci entries.
			// Edits preserves the pack ownership for existing lock entries.
			if tracked {
				output.Edits = append(output.Edits, change.Path)
			}
		}
		return output, nil
	}}
	result, err := genrun.Run(ctx, generator, nil, nil, o)
	if err != nil || o.DryRun || o.Diff {
		return result, err
	}
	// The generic runner assigns gen/ci to new files. Restore pack ownership so
	// upgrades continue to treat every workflow as a managed pack file.
	lockPath := filepath.Join(o.Root, "spinup.lock")
	lock, err := manifest.Load(lockPath)
	if err != nil {
		return result, err
	}
	changed := false
	for _, file := range rendered.Files {
		entry, ok := lock.Files[file.Path]
		if ok && entry.Template != file.Template {
			entry.Template = file.Template
			if err := lock.SetFile(file.Path, entry); err != nil {
				return result, err
			}
			changed = true
		}
	}
	if changed {
		return result, lock.Save(lockPath)
	}
	return result, nil
}
