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

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/merge"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

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
	newer, err := Render(in.New, in.Lock.Packs, in.NewAnswers)
	if err != nil {
		return nil, err
	}
	var older vfs.Plan
	if in.Old != nil {
		older, err = Render(in.Old, in.Lock.Packs, in.OldAnswers)
		if err != nil {
			return nil, err
		}
	}
	renames, err := RenamedFrom(in.New, in.Lock.Packs, in.NewAnswers)
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
		if from == "" || !tracked || targetTracked || !disks[from].exists || entry.Class == manifest.Seed || manifest.Class(file.Class) == manifest.Seed || slices.Contains(in.Lock.Tombstones, file.Path) {
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
		if _, consumed := sources[name]; consumed {
			continue
		}
		entry, tracked := in.Lock.Files[name]
		file, rendered := newer.Get(name)
		d := FileDecision{Path: name, Class: manifest.Class(file.Class), Template: file.Template, Mode: file.Mode, Data: file.Data}
		disk := disks[name]
		switch {
		case slices.Contains(in.Lock.Tombstones, name):
			d.Action, d.Reason = ActionKeep, "tombstone"
		case tracked && !disk.exists:
			d.Action, d.Reason = ActionKeep, "missing on disk"
		case entry.Class == manifest.Seed || d.Class == manifest.Seed:
			d.Action, d.Reason = ActionKeep, "seed"
			if !tracked && !disk.exists && rendered {
				d.Action, d.Reason = ActionCreate, ""
			}
		default:
			from := renames[name]
			switch {
			case sources[from] == name:
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
			if tracked {
				d.Class, d.Template = entry.Class, entry.Template
			}
			d.Data = nil
		}
		p.Files = append(p.Files, d)
	}
	return p, nil
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
