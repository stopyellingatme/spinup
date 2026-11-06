package vfs

import (
	"io/fs"
	"testing"
	"time"
)

type fakeDirInfo struct {
	mod  time.Time
	size int64
}

func (f fakeDirInfo) Name() string       { return "dir" }
func (f fakeDirInfo) Size() int64        { return f.size }
func (f fakeDirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o755 }
func (f fakeDirInfo) ModTime() time.Time { return f.mod }
func (f fakeDirInfo) IsDir() bool        { return true }
func (f fakeDirInfo) Sys() any           { return nil }

func TestListingUnchanged(t *testing.T) {
	fine := time.Date(2026, 11, 6, 12, 0, 0, 123456789, time.UTC)
	coarse := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name     string
		old, now fakeDirInfo
		want     bool
	}{
		{"same fine mtime", fakeDirInfo{fine, 96}, fakeDirInfo{fine, 96}, true},
		{"new mtime", fakeDirInfo{fine, 96}, fakeDirInfo{fine.Add(time.Nanosecond), 96}, false},
		{"new size", fakeDirInfo{fine, 96}, fakeDirInfo{fine, 128}, false},
		// A filesystem that stores whole seconds (HFS+, FAT, some network mounts)
		// keeps the same mtime after a change in the same second.
		{"same coarse mtime", fakeDirInfo{coarse, 96}, fakeDirInfo{coarse, 96}, false},
	} {
		if got := listingUnchanged(test.old, test.now); got != test.want {
			t.Errorf("%s: listingUnchanged = %v; want %v", test.name, got, test.want)
		}
	}
}
