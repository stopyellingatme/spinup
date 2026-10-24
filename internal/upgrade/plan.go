package upgrade

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/merge"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// PackSet selects the packs for one side of a render.
// Nil Names uses the locked selection. Nil FS uses the version's pack filesystem.
// FS can supply a combined filesystem of built-in and plugin packs.
type PackSet struct {
	// Names lists the packs in render order.
	Names []string
	// FS replaces the version's pack filesystem when supplied.
	FS fs.FS
}

func (p PackSet) resolve(v *templates.Version, fallback []string) (*templates.Version, []string) {
	names := p.Names
	if names == nil {
		names = fallback
	}
	if p.FS != nil && v != nil {
		copy := *v
		copy.Packs = p.FS
		v = &copy
	}
	return v, names
}

func planPacks(p *PackSet, v *templates.Version, fallback []string) (*templates.Version, []string) {
	if p == nil {
		return v, fallback
	}
	return p.resolve(v, fallback)
}

// PlanInput contains the disk, lock, template versions, and policy for one step.
type PlanInput struct {
	// Root is the project directory whose files are read.
	Root string
	// Lock is the current validated template snapshot.
	Lock *manifest.Lock
	// Old and New supply the previous and target templates. Old may be nil.
	Old, New *templates.Version
	// OldAnswers and NewAnswers are the render inputs for each version.
	OldAnswers, NewAnswers map[string]any
	// OldPacks and NewPacks select each side independently. Nil uses Lock.Packs.
	OldPacks, NewPacks *PackSet
	// Merger combines changed text without writing files.
	Merger merge.Merger
	// ForceManaged permits replacement of changed managed text.
	ForceManaged bool
}

type planDisk struct {
	data   []byte
	mode   fs.FileMode
	exists bool
}

// BuildPlan reads project files and returns sorted decisions without writing files.
func BuildPlan(ctx context.Context, in PlanInput) (*Plan, error) {
	if err := in.Lock.Validate(); err != nil {
		return nil, err
	}
	if err := planContext(ctx); err != nil {
		return nil, err
	}
	newVersion, newPacks := planPacks(in.NewPacks, in.New, in.Lock.Packs)
	oldVersion, oldPacks := planPacks(in.OldPacks, in.Old, in.Lock.Packs)
	newer, err := Render(newVersion, newPacks, in.NewAnswers)
	if err != nil {
		return nil, err
	}
	var older vfs.Plan
	if in.Old != nil {
		older, err = Render(oldVersion, oldPacks, in.OldAnswers)
		if err != nil {
			return nil, err
		}
	}
	renames, err := RenamedFrom(newVersion, newPacks, in.NewAnswers)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(in.Root)
	if err != nil {
		return nil, fmt.Errorf("open upgrade root %q: %w\nTry: select a readable project directory", in.Root, err)
	}
	defer root.Close()
	p := &Plan{From: in.Lock.TemplateVersion, To: in.New.Version}
	names := make(map[string]bool)
	for name := range in.Lock.Files {
		names[name] = true
	}
	for _, f := range newer.Files {
		names[f.Path] = true
	}
	for _, name := range in.Lock.Tombstones {
		names[name] = true
	}
	paths := make([]string, 0, len(names))
	for name := range names {
		paths = append(paths, name)
	}
	slices.Sort(paths)
	disks := make(map[string]planDisk, len(paths))
	for _, name := range paths {
		if err := planContext(ctx); err != nil {
			return nil, err
		}
		if slices.Contains(in.Lock.Tombstones, name) {
			continue
		}
		disk, err := readPlanDisk(root, name)
		if err != nil {
			return nil, err
		}
		disks[name] = disk
		if entry, tracked := in.Lock.Files[name]; tracked && !disk.exists {
			p.Tombstoned = append(p.Tombstoned, name)
			if entry.Class == manifest.Managed {
				p.MissingManaged = append(p.MissingManaged, name)
			}
		}
	}
	// Reserve rename sources before deciding removed paths, regardless of sort order.
	sources := make(map[string]string)
	for _, file := range newer.Sorted().Files {
		from := renames[file.Path]
		entry, tracked := in.Lock.Files[from]
		_, targetTracked := in.Lock.Files[file.Path]
		if from == "" || !tracked || targetTracked || !disks[from].exists || planClass(entry.Class, manifest.Class(file.Class)) == manifest.Seed || slices.Contains(in.Lock.Tombstones, file.Path) {
			continue
		}
		if previous, used := sources[from]; used {
			return nil, fmt.Errorf("plan rename %q: source %q also moves to %q\nTry: give each rename a unique source", file.Path, from, previous)
		}
		if _, rendered := newer.Get(from); rendered {
			return nil, fmt.Errorf("plan rename %q: source %q is still rendered\nTry: remove the source from the new templates", file.Path, from)
		}
		sources[from] = file.Path
	}
	for _, name := range paths {
		if target, consumed := sources[name]; consumed {
			file, _ := newer.Get(target)
			if disks[target].exists && !bytes.Equal(disks[target].data, file.Data) {
				entry := in.Lock.Files[name]
				p.Files = append(p.Files, FileDecision{Path: name, Class: entry.Class, Template: entry.Template, Action: ActionOrphan, Reason: "rename destination exists"})
			}
			continue
		}
		entry, tracked := in.Lock.Files[name]
		file, rendered := newer.Get(name)
		d := FileDecision{Path: name, Class: manifest.Class(file.Class), Template: file.Template, Mode: file.Mode, Data: file.Data}
		if tracked {
			d.Class = planClass(entry.Class, d.Class)
		}
		disk := disks[name]
		switch {
		case slices.Contains(in.Lock.Tombstones, name):
			d.Action, d.Reason = ActionKeep, "tombstone"
		case tracked && generatorTemplate(entry.Template) && !renderedPackTemplate(older, name, entry.Template):
			// A generator wrote this file last (ADR 0007 §1). Templates never re-render or
			// delete it; for a pack file such as routes_gen.go the generator's version wins.
			d.Class, d.Template = entry.Class, entry.Template
			d.Action, d.Reason = ActionKeep, "generator-owned"
		case tracked && !disk.exists:
			d.Action, d.Reason = ActionKeep, "missing on disk"
		case (!rendered && entry.Class == manifest.Seed) || d.Class == manifest.Seed:
			d.Action, d.Reason = ActionKeep, "seed"
			if !tracked && !disk.exists && rendered {
				d.Action, d.Reason = ActionCreate, ""
			}
		default:
			from := renames[name]
			switch {
			case sources[from] == name && disk.exists && !bytes.Equal(disk.data, file.Data):
				d, err = mergePlanFile(ctx, in.Merger, d, nil, disk.data, file.Data, "path exists")
			case sources[from] == name:
				d.Class = planClass(in.Lock.Files[from].Class, d.Class)
				d.From = from
				source := disks[from]

				d, err = decidePlanTracked(ctx, in, d, in.Lock.Files[from], source, older, from, true)
			case tracked && !rendered:
				d.Action = ActionOrphan
				if manifest.Hash(disk.data) == entry.Hash {
					d.Action = ActionDelete
				}
			case !tracked:
				switch {
				case !disk.exists:
					d.Action = ActionCreate
				case bytes.Equal(disk.data, file.Data):
					d.Action = ActionUnchanged
				default:
					d, err = mergePlanFile(ctx, in.Merger, d, nil, disk.data, file.Data, "path exists")
				}
			default:
				d, err = decidePlanTracked(ctx, in, d, entry, disk, older, name, false)
			}
		}
		if err != nil {
			return nil, err
		}
		if d.Action == ActionKeep || d.Action == ActionDelete || d.Action == ActionOrphan {
			if tracked && !rendered {
				d.Class, d.Template = entry.Class, entry.Template
			}
			d.Data = nil
		}
		p.Files = append(p.Files, d)
	}
	return p, nil
}

// planClass preserves an ejected file unless the template changes it to seed.
func planClass(old, newer manifest.Class) manifest.Class {
	if old == manifest.Scaffold && newer == manifest.Managed {
		return manifest.Scaffold
	}
	return newer
}

func planContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("plan upgrade: %w\nTry: retry the upgrade", err)
	}
	return nil
}

func readPlanDisk(root *os.Root, name string) (planDisk, error) {
	clean, err := vfs.CleanPath(name)
	if err != nil {
		return planDisk{}, err
	}
	if clean != name {
		return planDisk{}, fmt.Errorf("read upgrade path %q: path is not clean\nTry: repair the lock path", name)
	}
	info, err := root.Stat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return planDisk{}, nil
	}
	if err != nil {
		return planDisk{}, fmt.Errorf("inspect upgrade path %q: %w\nTry: use a readable file inside the project root", name, err)
	}
	if !info.Mode().IsRegular() {
		return planDisk{}, fmt.Errorf("read upgrade file %q: not a regular file\nTry: replace the path with a regular file", name)
	}
	file, err := root.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return planDisk{}, nil
	}
	if err != nil {
		return planDisk{}, fmt.Errorf("read upgrade path %q: %w\nTry: use a readable file inside the project root", name, err)
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return planDisk{}, fmt.Errorf("inspect upgrade file %q: %w\nTry: check the file permissions", name, err)
	}
	if !info.Mode().IsRegular() {
		return planDisk{}, fmt.Errorf("read upgrade file %q: not a regular file\nTry: replace the path with a regular file", name)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return planDisk{}, fmt.Errorf("read upgrade file %q: %w\nTry: check the file permissions", name, err)
	}
	return planDisk{data: manifest.Normalize(data), mode: info.Mode().Perm(), exists: true}, nil
}

func decidePlanTracked(ctx context.Context, in PlanInput, d FileDecision, entry manifest.FileEntry, disk planDisk, older vfs.Plan, basePath string, rename bool) (FileDecision, error) {
	if !rename && bytes.Equal(d.Data, disk.data) {
		d.Action = ActionUnchanged
		if d.Mode.Perm() != disk.mode {
			d.Action, d.Reason = ActionOverwrite, "mode"
		}
		return d, nil
	}
	if manifest.Hash(disk.data) == entry.Hash {
		d.Action = ActionOverwrite
		if rename {
			d.Action = ActionRename
		}
		return d, nil
	}
	base, hasBase := older.Get(basePath)
	if planUnmergeable(base.Data, disk.data, d.Data) {
		if rename {
			return FileDecision{}, fmt.Errorf("plan rename %q from %q: changed binary or large file cannot be merged\nTry: move the source to the destination before upgrading", d.Path, basePath)
		}
		d.Action, d.Reason = ActionKeep, "binary or large file changed on disk"
		return d, nil
	}
	if !rename && d.Class == manifest.Managed && in.ForceManaged {
		d.Action, d.Reason = ActionOverwrite, "forced"
		return d, nil
	}
	reason := ""
	if !hasBase {
		reason = "no base version"
	}
	merged, err := mergePlanFile(ctx, in.Merger, d, base.Data, disk.data, d.Data, reason)
	if err == nil && rename && merged.Action == ActionMerge {
		merged.Action = ActionRename
	}
	return merged, err
}

func planUnmergeable(inputs ...[]byte) bool {
	for _, data := range inputs {
		if manifest.IsBinary(data) || len(data) > merge.MaxSize {
			return true
		}
	}
	return false
}

func mergePlanFile(ctx context.Context, merger merge.Merger, d FileDecision, base, ours, theirs []byte, reason string) (FileDecision, error) {
	if planUnmergeable(base, ours, theirs) {
		d.Action, d.Reason = ActionKeep, "binary or large file changed on disk"
		return d, nil
	}
	if merger == nil {
		return FileDecision{}, fmt.Errorf("merge upgrade file %q: merger is nil\nTry: configure a text merger", d.Path)
	}
	result, err := merger.Merge(ctx, merge.Input{Path: d.Path, Base: base, Ours: ours, Theirs: theirs})
	if err != nil {
		return FileDecision{}, fmt.Errorf("merge upgrade file %q: %w\nTry: correct the merge failure and retry", d.Path, err)
	}
	d.Action, d.Data, d.Reason = ActionMerge, result.Data, reason
	if result.Conflicted {
		d.Action = ActionConflict
	}
	if reason == "path exists" {
		d.Action = ActionConflict
	}
	return d, nil
}

// renderedPackTemplate disambiguates legacy generator IDs from gen/ pack sources.
func renderedPackTemplate(older vfs.Plan, name, template string) bool {
	file, ok := older.Get(name)
	return ok && file.Template == template
}

// generatorTemplate accepts the reserved namespace and legacy generator IDs.
func generatorTemplate(name string) bool {
	if strings.HasPrefix(name, "gen/") {
		return true
	}
	parts := strings.SplitN(name, "/", 4)
	return len(parts) == 4 && parts[0] == "plugin" && (parts[2] == "@gen" || parts[2] == "@exec" || parts[2] == "gen")
}

// requireBase rejects a plan that merged changed files against an empty base because the old
// template version could not be loaded. Such a merge turns each changed file into a whole-file
// conflict, and the conflict hides the real difference between the two template versions.
func requireBase(plan *Plan, old *templates.Version, from string) error {
	if old != nil || plan == nil {
		return nil
	}
	var names []string
	for _, file := range plan.Files {
		if file.Action == ActionConflict && file.Reason == "no base version" {
			names = append(names, file.Path)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return fmt.Errorf("plan upgrade: template version %s is unavailable, so %d changed files have no merge base: %s\nTry: run spinup cache warm %s, or set SPINUP_TEMPLATES_DIR to a directory that holds that version", from, len(names), strings.Join(names, ", "), from)
}
