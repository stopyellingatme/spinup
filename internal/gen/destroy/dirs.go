package destroy

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// emptiedDirs returns the directories that become empty when the deleted files
// are gone, deepest first. A directory qualifies when each of its entries is a
// deleted file or another directory that qualifies. The project root, symbolic
// links, and directories that hold anything else are never listed.
func emptiedDirs(root string, deleted []string) ([]string, error) {
	gone := map[string]bool{}
	candidates := map[string]bool{}
	for _, p := range deleted {
		gone[p] = true
		for dir := path.Dir(p); dir != "." && dir != "/"; dir = path.Dir(dir) {
			candidates[dir] = true
		}
	}
	ordered := make([]string, 0, len(candidates))
	for dir := range candidates {
		ordered = append(ordered, dir)
	}
	// Deeper directories first, so a parent sees which children are already gone.
	sort.Slice(ordered, func(i, j int) bool {
		di, dj := strings.Count(ordered[i], "/"), strings.Count(ordered[j], "/")
		if di != dj {
			return di > dj
		}
		return ordered[i] < ordered[j]
	})
	var dirs []string
	for _, dir := range ordered {
		disk := filepath.Join(root, filepath.FromSlash(dir))
		info, err := os.Lstat(disk)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, destroyError(err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		entries, err := os.ReadDir(disk)
		if err != nil {
			return nil, destroyError(err)
		}
		empty := true
		for _, entry := range entries {
			if !gone[dir+"/"+entry.Name()] {
				empty = false
				break
			}
		}
		if empty {
			gone[dir] = true
			dirs = append(dirs, dir)
		}
	}
	return dirs, nil
}

// removeDirs removes the directories that emptiedDirs listed, deepest first.
// os.Remove refuses a directory that is not empty, so a file that appeared
// after the plan keeps its directory.
func removeDirs(root string, dirs []string) error {
	for _, dir := range dirs {
		err := os.Remove(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove empty directory %s: %w\nTry: check the directory, then delete it by hand", dir, err)
		}
	}
	return nil
}
