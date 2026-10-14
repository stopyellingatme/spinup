// Package vfs stores generated file changes before they reach the disk.
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
func CleanPath(p string) (string, error) {
	if p == "" || p == "." || path.IsAbs(p) || filepath.IsAbs(p) || strings.Contains(p, "\\") {
		return "", fmt.Errorf("invalid file path %q: use a relative slash-separated path", p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", fmt.Errorf("invalid file path %q: parent paths are not allowed", p)
		}
	}
	clean := path.Clean(p)
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("invalid file path %q: use a file path", p)
	}
	return clean, nil
}

// Add adds a file to the plan.
func (p *Plan) Add(f File) error {
	clean, err := CleanPath(f.Path)
	if err != nil {
		return err
	}
	for _, existing := range p.Files {
		if existing.Path == clean {
			return fmt.Errorf("add file %q: the plan already has this path", clean)
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
	merged := p.Sorted()
	for _, file := range other.Files {
		if _, ok := merged.Get(file.Path); ok {
			return Plan{}, fmt.Errorf("merge plans: path conflict at %q", file.Path)
		}
		if err := merged.Add(file); err != nil {
			return Plan{}, fmt.Errorf("merge plans: %w", err)
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
	root, err := resolvedRoot(root)
	if err != nil {
		return nil, err
	}
	changes := make([]Change, 0, len(p.Files))
	for _, file := range p.Sorted().Files {
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
		changes = append(changes, change)
	}
	return changes, nil
}

func diffFile(name, filename string, file File) (Change, error) {
	newMode := fileMode(file.Mode)
	change := Change{Path: name, New: bytes.Clone(file.Data), NewMode: newMode}
	info, err := os.Stat(filename)
	if errors.Is(err, fs.ErrNotExist) {
		if file.Delete {
			change.Kind = Unchanged
			return change, nil
		}
		change.Kind = Create
		change.UnifiedDiff = fileDiff(name, nil, file.Data)
		return change, nil
	}
	if err != nil {
		return Change{}, err
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
		change.UnifiedDiff = fileDiff(name, old, nil)
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
	change.UnifiedDiff = fileDiff(name, old, file.Data)
	return change, nil
}

// Apply applies a plan to root. It does not write when DryRun is true.
func Apply(root string, p Plan, opts ApplyOptions) ([]Change, error) {
	changes, err := Diff(root, p)
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
	err = copyAndSync(out, in)
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
	for _, part := range strings.Split(clean, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil || !within(root, resolved) {
				return "", fmt.Errorf("path %q uses a symlink outside the root", name)
			}
		}
	}
	return filename, nil
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

type diffLine struct {
	kind byte
	text string
}

func fileDiff(name string, old, new []byte) string {
	if isBinary(old) || isBinary(new) {
		return "Binary files differ"
	}
	oldLines := splitLines(old)
	newLines := splitLines(new)
	lines := lcsDiff(oldLines, newLines)
	changed := make([]bool, len(lines))
	for i, line := range lines {
		changed[i] = line.kind != ' '
	}
	hunks := make([][2]int, 0)
	for i, hasChange := range changed {
		if !hasChange {
			continue
		}
		start, end := max(0, i-3), min(len(lines), i+4)
		if len(hunks) > 0 && start <= hunks[len(hunks)-1][1] {
			if end > hunks[len(hunks)-1][1] {
				hunks[len(hunks)-1][1] = end
			}
			continue
		}
		hunks = append(hunks, [2]int{start, end})
	}
	if len(hunks) == 0 {
		return ""
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "--- a/%s\n+++ b/%s\n", name, name)
	for _, hunk := range hunks {
		oldStart, oldCount, newStart, newCount := hunkRange(lines, hunk[0], hunk[1])
		fmt.Fprintf(&builder, "@@ -%s +%s @@\n", rangeText(oldStart, oldCount), rangeText(newStart, newCount))
		for _, line := range lines[hunk[0]:hunk[1]] {
			builder.WriteByte(line.kind)
			builder.WriteString(line.text)
			builder.WriteByte('\n')
		}
	}
	return builder.String()
}

func isBinary(data []byte) bool {
	if len(data) > 8000 {
		data = data[:8000]
	}
	return bytes.IndexByte(data, 0) >= 0
}

func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	text := string(data)
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return []string{""}
	}
	return strings.Split(text, "\n")
}

func lcsDiff(old, new []string) []diffLine {
	lengths := make([][]int, len(old)+1)
	for i := range lengths {
		lengths[i] = make([]int, len(new)+1)
	}
	for i := len(old) - 1; i >= 0; i-- {
		for j := len(new) - 1; j >= 0; j-- {
			if old[i] == new[j] {
				lengths[i][j] = lengths[i+1][j+1] + 1
			} else {
				lengths[i][j] = max(lengths[i+1][j], lengths[i][j+1])
			}
		}
	}
	lines := make([]diffLine, 0, len(old)+len(new))
	for i, j := 0, 0; i < len(old) || j < len(new); {
		switch {
		case i == len(old):
			lines = append(lines, diffLine{kind: '+', text: new[j]})
			j++
		case j == len(new):
			lines = append(lines, diffLine{kind: '-', text: old[i]})
			i++
		case old[i] == new[j]:
			lines = append(lines, diffLine{kind: ' ', text: old[i]})
			i++
			j++
		case lengths[i+1][j] >= lengths[i][j+1]:
			lines = append(lines, diffLine{kind: '-', text: old[i]})
			i++
		default:
			lines = append(lines, diffLine{kind: '+', text: new[j]})
			j++
		}
	}
	return lines
}

func hunkRange(lines []diffLine, start, end int) (int, int, int, int) {
	oldStart, newStart := 1, 1
	for _, line := range lines[:start] {
		if line.kind != '+' {
			oldStart++
		}
		if line.kind != '-' {
			newStart++
		}
	}
	oldCount, newCount := 0, 0
	for _, line := range lines[start:end] {
		if line.kind != '+' {
			oldCount++
		}
		if line.kind != '-' {
			newCount++
		}
	}
	if oldCount == 0 {
		oldStart--
	}
	if newCount == 0 {
		newStart--
	}
	return oldStart, oldCount, newStart, newCount
}

func rangeText(start, count int) string {
	if count == 1 {
		return fmt.Sprint(start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}

func applyError(name string, err error) error {
	return fmt.Errorf("apply %q failed: %w\nTry: check the target path and its permissions", name, err)
}
