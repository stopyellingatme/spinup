package pack

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"unicode"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

const (
	maxBytes = 5 << 20
	maxFiles = 1000
)

type tree struct{ plan vfs.Plan }

// readTree creates an immutable snapshot before metadata or templates are read.
// os.Root prevents a concurrent path replacement from escaping the source root.
func readTree(dir string) (tree, error) {
	var result tree
	info, err := os.Lstat(dir)
	if err != nil {
		return result, problem("inspect plugin tree", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, problem("inspect plugin tree", fmt.Errorf("%q is not a directory without symlinks", dir))
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return result, problem("open plugin tree", err)
	}
	defer root.Close()
	var size, normalizedSize int64
	entries := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		// Git administration is never part of a plugin artifact.
		if name == ".git" && entry.IsDir() {
			return fs.SkipDir
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("symlink %q is forbidden", name)
		}
		clean, err := vfs.CleanPath(name)
		if err != nil || clean != name {
			return fmt.Errorf("unsafe plugin path %q", name)
		}
		entries++
		if entries > maxFiles {
			return fmt.Errorf("plugin exceeds %d entries", maxFiles)
		}
		if name == ".gitmodules" || name == ".git" {
			return fmt.Errorf("git submodules and git indirection are forbidden")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("plugin entry %q is not a regular file", name)
		}
		if info.Size() > maxBytes-size {
			return fmt.Errorf("plugin exceeds %d bytes", maxBytes)
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxBytes-size+1))
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		size += int64(len(data))
		if size > maxBytes {
			return fmt.Errorf("plugin exceeds %d bytes", maxBytes)
		}
		mode := fs.FileMode(0644)
		if info.Mode().Perm()&0111 != 0 {
			mode = 0755
		}
		normalized := manifest.Normalize(data)
		normalizedSize += int64(len(normalized))
		if normalizedSize > maxBytes {
			return fmt.Errorf("normalized plugin exceeds %d bytes", maxBytes)
		}
		return result.plan.Add(vfs.File{Path: name, Data: normalized, Mode: mode})
	})
	if err != nil {
		return tree{}, problem("read plugin tree", err)
	}
	return result, nil
}

func (t tree) hash() string {
	sum := sha256.New()
	// WalkDir supplies sorted paths. Lengths prevent ambiguous concatenations.
	for _, file := range t.plan.Files {
		_, _ = fmt.Fprintf(sum, "%d:%s:%04o:%d:", len(file.Path), file.Path, file.Mode, len(file.Data))
		_, _ = sum.Write(file.Data)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func (t tree) fs() fs.FS { return t.plan.FS() }

func problem(action string, err error) error {
	return fmt.Errorf("%s: %w\nTry: inspect the plugin source, then run spinup plugin add or update --trust", action, err)
}

func token(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}
