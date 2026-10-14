// Package vfs stores generated file changes before they reach the disk.
//
// Apply is atomic for each file. It rolls back when one of its own operations
// fails. A process kill during an apply can leave a partial tree. Callers that
// need full recovery, such as upgrade, run inside a git worktree.
package vfs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing/fstest"
	"unicode"
)

const defaultMode fs.FileMode = 0o644

// File describes one staged file change.
type File struct {
	Path     string
	Data     []byte
	Mode     fs.FileMode
	Delete   bool
	Class    string
	Template string
}

// Plan holds the staged file changes.
type Plan struct {
	Files []File
}

// CleanPath validates and normalizes a slash-separated relative file path.
// It rejects colons, backslashes, NUL, and control characters. It also rejects
// Windows device names, regardless of case or extension, and segments that end
// with a space or dot.
func CleanPath(p string) (string, error) {
	if p == "" || p == "." || path.IsAbs(p) || filepath.IsAbs(p) {
		return "", invalidPath(p, "use a relative slash-separated path")
	}
	if strings.ContainsAny(p, ":\\") || strings.ContainsFunc(p, unicode.IsControl) {
		return "", invalidPath(p, "colons, backslashes, and control characters are not allowed")
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", invalidPath(p, "parent paths are not allowed")
		}
		if strings.HasSuffix(part, " ") || strings.HasSuffix(part, ".") {
			return "", invalidPath(p, "path segments cannot end with a space or dot")
		}
		base, _, _ := strings.Cut(part, ".")
		if reservedDevice(base) {
			return "", invalidPath(p, "Windows device names are not allowed")
		}
	}
	clean := path.Clean(p)
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") {
		return "", invalidPath(p, "use a file path")
	}
	return clean, nil
}

func invalidPath(name, reason string) error {
	return fmt.Errorf("invalid file path %q: %s\nTry: use a portable relative file path", name, reason)
}

func reservedDevice(base string) bool {
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(base) == 4 && base[3] >= '1' && base[3] <= '9' &&
		(strings.EqualFold(base[:3], "COM") || strings.EqualFold(base[:3], "LPT"))
}

// Add adds a file to the plan.
func (p *Plan) Add(f File) error {
	clean, err := CleanPath(f.Path)
	if err != nil {
		return err
	}
	for _, existing := range p.Files {
		if strings.EqualFold(existing.Path, clean) {
			return fmt.Errorf("add file %q: path conflicts with %q under case folding\nTry: give each file a distinct path", clean, existing.Path)
		}
	}
	f.Path = clean
	f.Data = bytes.Clone(f.Data)
	p.Files = append(p.Files, f)
	return nil
}

// Get returns the file with path.
func (p Plan) Get(name string) (File, bool) {
	clean, err := CleanPath(name)
	if err != nil {
		return File{}, false
	}
	for _, file := range p.Files {
		if file.Path == clean {
			file.Data = bytes.Clone(file.Data)
			return file, true
		}
	}
	return File{}, false
}

// Paths returns the sorted file paths in the plan.
func (p Plan) Paths() []string {
	paths := make([]string, 0, len(p.Files))
	for _, file := range p.Files {
		paths = append(paths, file.Path)
	}
	sort.Strings(paths)
	return paths
}

// Sorted returns a copy of the plan sorted by path.
func (p Plan) Sorted() Plan {
	files := make([]File, len(p.Files))
	for i, file := range p.Files {
		files[i] = file
		files[i].Data = bytes.Clone(file.Data)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return Plan{Files: files}
}

// Merge combines two plans when their paths do not conflict.
func (p Plan) Merge(other Plan) (Plan, error) {
	var merged Plan
	for _, plan := range []Plan{p, other} {
		for _, file := range plan.Files {
			if err := merged.Add(file); err != nil {
				return Plan{}, fmt.Errorf("merge plans: %w", err)
			}
		}
	}
	return merged.Sorted(), nil
}

// ChangeKind identifies a difference between a plan and the disk.
type ChangeKind int

const (
	// Unchanged identifies an identical file.
	Unchanged ChangeKind = iota
	// Create identifies a new file.
	Create
	// Update identifies changed file content.
	Update
	// Remove identifies a removed file.
	Remove
	// ModeOnly identifies a permissions-only change.
	ModeOnly
)

// String returns the name of the change kind.
func (k ChangeKind) String() string {
	switch k {
	case Unchanged:
		return "unchanged"
	case Create:
		return "create"
	case Update:
		return "update"
	case Remove:
		return "remove"
	case ModeOnly:
		return "mode-only"
	default:
		return fmt.Sprintf("ChangeKind(%d)", k)
	}
}

// Change describes one comparison result.
type Change struct {
	Path        string
	Kind        ChangeKind
	Old         []byte
	New         []byte
	OldMode     fs.FileMode
	NewMode     fs.FileMode
	UnifiedDiff string
}

// Diff compares the plan with files below root.
func Diff(root string, p Plan) ([]Change, error) {
	return compare(root, p, true)
}

func compare(root string, p Plan, includeDiff bool) ([]Change, error) {
	root, err := resolvedRoot(root)
	if err != nil {
		return nil, err
	}
	p, err = p.Merge(Plan{})
	if err != nil {
		return nil, err
	}
	changes := make([]Change, 0, len(p.Files))
	for _, file := range p.Files {
		clean, err := CleanPath(file.Path)
		if err != nil {
			return nil, err
		}
		filename, err := safePath(root, clean)
		if err != nil {
			return nil, err
		}
		change, err := diffFile(clean, filename, file)
		if err != nil {
			return nil, fmt.Errorf("diff %q: %w", clean, err)
		}
		if includeDiff && (change.Kind == Create || change.Kind == Update || change.Kind == Remove) {
			change.UnifiedDiff = fileDiff(clean, change.Old, change.New)
		}
		changes = append(changes, change)
	}
	return changes, nil
}

func diffFile(name, filename string, file File) (Change, error) {
	newMode := fileMode(file.Mode)
	change := Change{Path: name, New: bytes.Clone(file.Data), NewMode: newMode}
	info, err := os.Lstat(filename)
	if errors.Is(err, fs.ErrNotExist) {
		if file.Delete {
			change.Kind = Unchanged
			return change, nil
		}
		change.Kind = Create
		return change, nil
	}
	if err != nil {
		return Change{}, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return Change{}, symlinkError(name)
	}
	if !info.Mode().IsRegular() {
		return Change{}, fmt.Errorf("target is not a regular file")
	}
	old, err := os.ReadFile(filename)
	if err != nil {
		return Change{}, err
	}
	change.Old, change.OldMode = old, info.Mode().Perm()
	if file.Delete {
		change.Kind = Remove
		change.New, change.NewMode = nil, 0
		return change, nil
	}
	if bytes.Equal(old, file.Data) {
		if change.OldMode == newMode {
			change.Kind = Unchanged
		} else {
			change.Kind = ModeOnly
		}
		return change, nil
	}
	change.Kind = Update
	return change, nil
}

// Apply applies a plan to root. It does not write when DryRun is true.
// Returned changes contain text diffs only when DryRun is true.
func Apply(root string, p Plan, opts ApplyOptions) ([]Change, error) {
	changes, err := compare(root, p, opts.DryRun)
	if err != nil || opts.DryRun {
		return changes, err
	}
	resolved, err := resolvedRoot(root)
	if err != nil {
		return nil, err
	}
	backupDir, err := os.MkdirTemp(resolved, ".spinup-vfs-")
	if err != nil {
		return nil, fmt.Errorf("prepare atomic apply: %w", err)
	}
	defer os.RemoveAll(backupDir)

	applied := make([]appliedChange, 0, len(changes))
	createdDirs := make([]string, 0)
	for _, change := range changes {
		if change.Kind == Unchanged {
			continue
		}
		if applyHook != nil {
			if err := applyHook(change.Path); err != nil {
				rollback(resolved, applied, createdDirs)
				return changes, applyError(change.Path, err)
			}
		}
		filename, err := safePath(resolved, change.Path)
		if err == nil && change.Kind != Remove {
			createdDirs, err = ensureParents(resolved, change.Path, createdDirs)
		}
		if err == nil {
			err = applyChange(filename, backupDir, change, &applied)
		}
		if err != nil {
			rollback(resolved, applied, createdDirs)
			return changes, applyError(change.Path, err)
		}
	}
	return changes, nil
}

// ApplyOptions configures Apply.
type ApplyOptions struct {
	DryRun bool
}

// FS returns a read-only file system view of the plan.
func (p Plan) FS() fs.FS {
	files := make(fstest.MapFS, len(p.Files))
	for _, file := range p.Files {
		if file.Delete {
			continue
		}
		files[file.Path] = &fstest.MapFile{Data: bytes.Clone(file.Data), Mode: fileMode(file.Mode)}
	}
	return files
}

var applyHook func(path string) error

type appliedChange struct {
	filename string
	backup   string
	created  bool
	deleted  bool
	oldMode  fs.FileMode
}

func applyChange(filename, backupDir string, change Change, applied *[]appliedChange) error {
	entry := appliedChange{filename: filename, oldMode: change.OldMode}
	if change.Kind == ModeOnly {
		if err := os.Chmod(filename, change.NewMode); err != nil {
			return err
		}
		*applied = append(*applied, entry)
		return nil
	}
	if change.Kind == Remove {
		entry.backup = filepath.Join(backupDir, fmt.Sprintf("%d", len(*applied)))
		if err := os.Rename(filename, entry.backup); err != nil {
			return err
		}
		entry.deleted = true
		*applied = append(*applied, entry)
		return nil
	}
	if change.Kind == Update {
		entry.backup = filepath.Join(backupDir, fmt.Sprintf("%d", len(*applied)))
		if err := copyFile(filename, entry.backup, change.OldMode); err != nil {
			return err
		}
	} else {
		entry.created = true
	}
	temporary, err := os.CreateTemp(filepath.Dir(filename), ".spinup-vfs-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	err = writeAndSync(temporary, change.New, change.NewMode)
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(temporaryName, filename); err != nil {
		return err
	}
	*applied = append(*applied, entry)
	return nil
}

func rollback(root string, applied []appliedChange, dirs []string) {
	for i := len(applied) - 1; i >= 0; i-- {
		entry := applied[i]
		switch {
		case entry.created:
			_ = os.Remove(entry.filename)
		case entry.deleted:
			_ = os.Rename(entry.backup, entry.filename)
		case entry.backup != "":
			_ = os.Remove(entry.filename)
			_ = os.Rename(entry.backup, entry.filename)
		default:
			_ = os.Chmod(entry.filename, entry.oldMode)
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i])
	}
	_ = root
}

func ensureParents(root, name string, known []string) ([]string, error) {
	dir := path.Dir(name)
	if dir == "." {
		return known, nil
	}
	parts := strings.Split(dir, "/")
	for i := range parts {
		current := filepath.Join(append([]string{root}, parts[:i+1]...)...)
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
				return known, fmt.Errorf("parent %q is not a directory", path.Join(parts[:i+1]...))
			}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return known, err
		}
		if err := os.Mkdir(current, 0o755); err != nil {
			return known, err
		}
		known = append(known, current)
	}
	return known, nil
}

func copyFile(source, destination string, mode fs.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	err = out.Chmod(mode)
	if err == nil {
		err = copyAndSync(out, in)
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return err
}

func resolvedRoot(root string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("read root %q: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("root %q is not a directory", root)
	}
	return filepath.EvalSymlinks(root)
}

func safePath(root, name string) (string, error) {
	clean, err := CleanPath(name)
	if err != nil {
		return "", err
	}
	filename := filepath.Join(root, filepath.FromSlash(clean))
	current := root
	parts := strings.Split(clean, "/")
	for i, part := range parts {
		entries, err := os.ReadDir(current)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("inspect directory for %q: %w\nTry: check the target directory and its permissions", name, err)
		}
		for _, entry := range entries {
			if entry.Name() != part && strings.EqualFold(entry.Name(), part) {
				existing := path.Join(path.Join(parts[:i]...), entry.Name())
				return "", fmt.Errorf("path %q conflicts with existing entry %q under case folding\nTry: use the existing spelling or rename the entry", name, existing)
			}
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			if i == len(parts)-1 {
				return "", symlinkError(name)
			}
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil || !within(root, resolved) {
				return "", fmt.Errorf("path %q uses a symlink outside the root\nTry: replace the symlink with a directory inside the root", name)
			}
		}
	}
	return filename, nil
}

func symlinkError(name string) error {
	return fmt.Errorf("path %q is a symlink\nTry: replace the symlink with a regular file, or remove it from the template", name)
}

func within(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func fileMode(mode fs.FileMode) fs.FileMode {
	if mode == 0 {
		return defaultMode
	}
	return mode.Perm()
}

func writeAndSync(file *os.File, data []byte, mode fs.FileMode) error {
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	return file.Sync()
}

func copyAndSync(destination *os.File, source *os.File) error {
	if _, err := io.Copy(destination, source); err != nil {
		return err
	}
	return destination.Sync()
}

func applyError(name string, err error) error {
	if strings.Contains(err.Error(), "\nTry:") {
		return fmt.Errorf("apply %q failed: %w", name, err)
	}
	return fmt.Errorf("apply %q failed: %w\nTry: check the target path and its permissions", name, err)
}
