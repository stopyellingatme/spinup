package manifest

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// FileState describes a tracked file's content relative to its locked hash.
type FileState int

const (
	// Unchanged means normalized content matches the locked hash.
	Unchanged FileState = iota
	// Modified means normalized content differs from the locked hash.
	Modified
	// Missing means the tracked file does not exist on disk.
	Missing
)

// String returns the readable name of the file state.
func (s FileState) String() string {
	switch s {
	case Unchanged:
		return "unchanged"
	case Modified:
		return "modified"
	case Missing:
		return "missing"
	default:
		return fmt.Sprintf("FileState(%d)", s)
	}
}

// FileStatus combines a tracked file's path, lock entry, and current state.
type FileStatus struct {
	// Path is the file's relative path.
	Path string
	// Entry contains the file's locked metadata.
	Entry FileEntry
	// State describes the content comparison.
	State FileState
}

// Status compares normalized disk content with locked hashes in path order.
// It skips seed files and tombstones. File access stays within root.
func (l *Lock) Status(root string) ([]FileStatus, error) {
	paths, err := l.comparedPaths()
	if err != nil {
		return nil, err
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, diskError("check file status", root, err)
	}
	defer func() { _ = dir.Close() }()
	statuses := make([]FileStatus, 0, len(paths))
	for _, path := range paths {
		entry := l.Files[path]
		status := FileStatus{Path: path, Entry: entry, State: Unchanged}
		data, err := readTrackedFile(dir, path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			status.State = Missing
		case err != nil:
			return nil, diskError("check file status", path, err)
		case Hash(data) != entry.Hash:
			status.State = Modified
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

// ReconcileMissing tombstones absent scaffold files and reports absent managed files.
// It skips seed files and tombstones. An error leaves the lock unchanged.
func (l *Lock) ReconcileMissing(root string) (tombstoned []string, missingManaged []string, err error) {
	paths, err := l.comparedPaths()
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, diskError("reconcile missing files", root, err)
	}
	defer func() { _ = dir.Close() }()
	tombstoned = []string{}
	missingManaged = []string{}
	for _, path := range paths {
		info, err := dir.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			if l.Files[path].Class == Scaffold {
				tombstoned = append(tombstoned, path)
			} else {
				missingManaged = append(missingManaged, path)
			}
			continue
		}
		if err == nil && !info.Mode().IsRegular() {
			err = fmt.Errorf("tracked path is not a regular file")
		}
		if err != nil {
			return nil, nil, diskError("reconcile missing files", path, err)
		}
	}
	for _, path := range tombstoned {
		l.Tombstone(path)
	}
	return tombstoned, missingManaged, nil
}

func (l *Lock) comparedPaths() ([]string, error) {
	if l == nil {
		return nil, fmt.Errorf("compare tracked files: the lock is nil\nTry: load a valid spinup.lock")
	}
	tombstones := make(map[string]bool, len(l.Tombstones))
	for _, path := range l.Tombstones {
		tombstones[path] = true
	}
	var problems []error
	paths := make([]string, 0, len(l.Files))
	for _, path := range sortedFilePaths(l.Files) {
		entry := l.Files[path]
		if entry.Class == Seed || tombstones[path] {
			continue
		}
		problems = append(problems, entryProblems(path, entry)...)
		paths = append(paths, path)
	}
	if err := errors.Join(problems...); err != nil {
		return nil, fmt.Errorf("compare tracked files: %w\nTry: repair the listed entries in spinup.lock", err)
	}
	return paths, nil
}

func readTrackedFile(root *os.Root, path string) ([]byte, error) {
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("tracked path is not a regular file")
	}
	return io.ReadAll(file)
}

func diskError(action, path string, err error) error {
	return fmt.Errorf("%s at %q: %w\nTry: check the project path and file permissions", action, path, err)
}
