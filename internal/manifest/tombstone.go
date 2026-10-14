package manifest

import (
	"errors"
	"fmt"
	"slices"
)

// Tombstone removes the live entry and records the path once as a tombstone.
func (l *Lock) Tombstone(path string) {
	delete(l.Files, path)
	l.Tombstones = sortedUnique(append(l.Tombstones, path))
}

// Restore removes the path's tombstone without creating a file entry.
func (l *Lock) Restore(path string) {
	l.Tombstones = slices.DeleteFunc(l.Tombstones, func(value string) bool { return value == path })
}

// SetFile validates and records an entry, then clears its tombstone.
// An invalid entry leaves the lock unchanged.
func (l *Lock) SetFile(path string, e FileEntry) error {
	if l == nil {
		return fmt.Errorf("set tracked file %q: the lock is nil\nTry: create a lock with manifest.New", path)
	}
	if err := errors.Join(entryProblems(path, e)...); err != nil {
		return fmt.Errorf("set tracked file %q: %w\nTry: provide a valid relative path and file entry", path, err)
	}
	if l.Files == nil {
		l.Files = make(map[string]FileEntry)
	}
	l.Files[path] = e
	l.Restore(path)
	return nil
}
