package manifest

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// Validate returns all schema, file, and path problems in a single error.
func (l *Lock) Validate() error {
	if l == nil {
		return fmt.Errorf("validate spinup.lock: the lock is nil\nTry: create a lock with manifest.New")
	}
	var problems []error
	if l.Schema != currentSchemaVersion {
		problems = append(problems, fmt.Errorf("schema %d is unsupported (expected %d)", l.Schema, currentSchemaVersion))
	}
	for _, path := range sortedFilePaths(l.Files) {
		problems = append(problems, entryProblems(path, l.Files[path])...)
	}
	for _, path := range sortedUnique(l.Tombstones) {
		if !cleanPath(path) {
			problems = append(problems, fmt.Errorf("tombstone path %q must be a clean relative path", path))
		}
		if _, ok := l.Files[path]; ok {
			problems = append(problems, fmt.Errorf("tombstone %q is also a live entry", path))
		}
	}
	for _, path := range sortedUnique(l.Orphans) {
		if !cleanPath(path) {
			problems = append(problems, fmt.Errorf("orphan path %q must be a clean relative path", path))
		}
	}
	if err := errors.Join(problems...); err != nil {
		return fmt.Errorf("validate spinup.lock: %w\nTry: repair the listed entries in spinup.lock", err)
	}
	return nil
}

func entryProblems(path string, entry FileEntry) []error {
	var problems []error
	if !cleanPath(path) {
		problems = append(problems, fmt.Errorf("file path %q must be a clean relative path", path))
	}
	if !entry.Class.Valid() {
		problems = append(problems, fmt.Errorf("file %q has invalid class %q", path, entry.Class))
	}
	if (entry.Hash != "" || entry.Class != Seed) && !validHash(entry.Hash) {
		problems = append(problems, fmt.Errorf("file %q has invalid hash %q (expected sha256: and 64 lowercase hex digits)", path, entry.Hash))
	}
	if (entry.Mode != "" || entry.Class != Seed) && !validMode(entry.Mode) {
		problems = append(problems, fmt.Errorf("file %q has invalid mode %q (expected four octal digits, such as 0644)", path, entry.Mode))
	}
	if (entry.Template != "" || entry.Class != Seed) && !cleanPath(entry.Template) {
		problems = append(problems, fmt.Errorf("file %q has invalid template path %q (expected a clean relative path)", path, entry.Template))
	}
	return problems
}

func cleanPath(path string) bool {
	return path != "." && fs.ValidPath(path) && !strings.ContainsAny(path, "\\:\x00")
}

func validHash(hash string) bool {
	if len(hash) != len("sha256:")+64 || !strings.HasPrefix(hash, "sha256:") {
		return false
	}
	for _, c := range hash[len("sha256:"):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validMode(mode string) bool {
	if len(mode) != 4 || mode[0] != '0' {
		return false
	}
	for _, c := range mode[1:] {
		if c < '0' || c > '7' {
			return false
		}
	}
	return true
}

func sortedFilePaths(files map[string]FileEntry) []string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths
}

func sortedUnique(values []string) []string {
	result := append([]string{}, values...)
	slices.Sort(result)
	return slices.Compact(result)
}
