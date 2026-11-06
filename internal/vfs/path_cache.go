package vfs

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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
	// On Windows, os.Stat reads directory metadata from the parent's index, and
	// NTFS updates that copy late. A new entry then does not change ModTime, so
	// Windows reads the directory each time.
	if cached, ok := c[dir]; ok && runtime.GOOS != "windows" && os.SameFile(info, cached.info) &&
		listingUnchanged(cached.info, info) {
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

// listingUnchanged reports whether a directory with the metadata now still
// has the listing that was read with the metadata old. It trusts only a
// modification time with sub-second precision. A filesystem that stores whole
// seconds (HFS+, FAT, some network mounts) can change the directory in the
// same second and keep the same time. Such a time has zero nanoseconds,
// except for one value in a billion, and that case only costs a ReadDir.
func listingUnchanged(old, now fs.FileInfo) bool {
	return now.ModTime().Nanosecond() != 0 &&
		now.ModTime().Equal(old.ModTime()) && now.Size() == old.Size()
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
