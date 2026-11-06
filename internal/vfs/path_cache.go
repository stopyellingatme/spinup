package vfs

import (
	"io/fs"
	"os"
	"path/filepath"
)

// pathCache lasts for one comparison or apply pass. It caches directory names,
// but safePath still checks each path component for symlinks on every call.
type pathCache map[string]checkedDirectory

type checkedDirectory struct {
	info  fs.FileInfo
	names []string
}

func (c pathCache) readDir(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if cached, ok := c[dir]; ok && os.SameFile(info, cached.info) &&
		info.ModTime().Equal(cached.info.ModTime()) && info.Size() == cached.info.Size() {
		return cached.names, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	c[dir] = checkedDirectory{info: info, names: names}
	return names, nil
}

// record updates a cached listing after Apply creates or removes an entry.
// Refreshing the metadata avoids a new ReadDir after each write. An unexpected
// directory change invalidates the cache on the next readDir call.
func (c pathCache) record(filename string, removed bool) {
	dir := filepath.Dir(filename)
	cached, ok := c[dir]
	if !ok {
		return
	}
	info, err := os.Stat(dir)
	if err != nil {
		delete(c, dir)
		return
	}
	name := filepath.Base(filename)
	found := false
	for i, existing := range cached.names {
		if existing == name {
			found = true
			if removed {
				cached.names = append(cached.names[:i], cached.names[i+1:]...)
			}
			break
		}
	}
	if !found && !removed {
		cached.names = append(cached.names, name)
	}
	cached.info = info
	c[dir] = cached
}
