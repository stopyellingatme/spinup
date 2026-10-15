package store

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
)

const (
	maxEntries  = 10000
	maxUnpacked = 50 << 20
)

// WriteArchive writes sorted template files with stable metadata and zstd compression.
func WriteArchive(w io.Writer, packs fs.FS, stackYAML, kitNotes []byte) error {
	zw, err := zstd.NewWriter(w, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return archiveError(err)
	}
	defer func() { _ = zw.Close() }()
	tw := tar.NewWriter(zw)
	entries := map[string]*memoryEntry{
		"stackversions.yaml": {name: "stackversions.yaml", data: stackYAML, mode: 0644},
	}
	if kitNotes != nil {
		entries["MIGRATION.json"] = &memoryEntry{name: "MIGRATION.json", data: kitNotes, mode: 0644}
	}
	err = fs.WalkDir(packs, ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported file %q", name)
		}
		entry := &memoryEntry{name: path.Base(name), mode: info.Mode()}
		if !info.IsDir() {
			entry.data, err = fs.ReadFile(packs, name)
			if err != nil {
				return err
			}
		}
		entries["packs/"+name] = entry
		return nil
	})
	if err != nil {
		return archiveError(err)
	}
	entries["packs"] = &memoryEntry{name: "packs", mode: fs.ModeDir | 0755}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	var size int64
	for _, name := range names {
		entry := entries[name]
		size += int64(len(entry.data))
		if len(names) > maxEntries || size > maxUnpacked {
			return archiveError(errors.New("archive exceeds the entry or size limit"))
		}
		h, err := tar.FileInfoHeader(entry, "")
		if err != nil {
			return archiveError(err)
		}
		h.Name = name
		if err := tw.WriteHeader(h); err != nil {
			return archiveError(err)
		}
		if _, err := tw.Write(entry.data); err != nil {
			return archiveError(err)
		}
	}
	if err := tw.Close(); err != nil {
		return archiveError(err)
	}
	if err := zw.Close(); err != nil {
		return archiveError(err)
	}
	return nil
}

// ReadArchive validates and expands a template archive into an in-memory file system.
func ReadArchive(data []byte, version string) (*templates.Version, error) {
	if err := validateVersion(version); err != nil {
		return nil, err
	}
	zr, err := zstd.NewReader(bytes.NewReader(data), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(64<<20))
	if err != nil {
		return nil, archiveError(err)
	}
	defer zr.Close()
	// Bound the whole tar stream, including headers and padding, as well as file data.
	expanded := &io.LimitedReader{R: zr, N: maxArchive + 1}
	tr := tar.NewReader(expanded)
	files := memoryFS{".": &memoryEntry{name: ".", mode: fs.ModeDir | 0755}}
	seen := map[string]bool{}
	var total int64
	var stack, notes []byte
	hasPacks := false
	for count := 0; ; count++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, archiveError(err)
		}
		if count >= maxEntries {
			return nil, archiveError(errors.New("archive has more than 10000 entries"))
		}
		name := strings.TrimSuffix(h.Name, "/")
		if !fs.ValidPath(name) || strings.Contains(name, "\\") || strings.Contains(name, ":") {
			return nil, archiveError(fmt.Errorf("unsafe archive path %q", h.Name))
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
			return nil, archiveError(fmt.Errorf("unsupported archive entry %q", name))
		}
		isPack := strings.HasPrefix(name, "packs/")
		if !isPack && name != "packs" && name != "stackversions.yaml" && name != "MIGRATION.json" {
			return nil, archiveError(fmt.Errorf("unexpected archive entry %q", name))
		}
		if (name == "packs" && h.Typeflag != tar.TypeDir) || (!isPack && name != "packs" && h.Typeflag != tar.TypeReg) {
			return nil, archiveError(fmt.Errorf("invalid entry type for %q", name))
		}
		if seen[name] {
			return nil, archiveError(fmt.Errorf("duplicate archive entry %q", name))
		}
		seen[name] = true
		if h.Size < 0 || h.Size > maxUnpacked-total {
			return nil, archiveError(errors.New("archive exceeds 50 MiB unpacked"))
		}
		total += h.Size
		content, err := io.ReadAll(tr)
		if err != nil {
			return nil, archiveError(err)
		}
		switch name {
		case "stackversions.yaml":
			stack = content
		case "MIGRATION.json":
			notes = content
		case "packs":
			hasPacks = true
		default:
			hasPacks = true
			name = strings.TrimPrefix(name, "packs/")
			mode := h.FileInfo().Mode()
			if err := files.add(name, content, mode); err != nil {
				return nil, archiveError(err)
			}
		}
	}
	// Consume tar padding and the zstd trailer. Reject a hidden second archive.
	padding := make([]byte, 32<<10)
	for {
		n, err := expanded.Read(padding)
		if len(bytes.Trim(padding[:n], "\x00")) != 0 {
			return nil, archiveError(errors.New("unexpected data after the tar archive"))
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, archiveError(err)
		}
	}
	if expanded.N == 0 {
		return nil, archiveError(errors.New("expanded archive exceeds the size limit"))
	}
	if stack == nil || !hasPacks {
		return nil, archiveError(errors.New("archive requires packs/ and stackversions.yaml"))
	}
	pinned, err := stackversions.Parse(stack)
	if err != nil {
		return nil, archiveError(err)
	}
	return &templates.Version{Version: version, Packs: files, Stack: pinned, KitMigration: notes}, nil
}

func archiveError(err error) error {
	return fmt.Errorf("cannot process template archive: %w\nTry: use a valid templates archive", err)
}

type memoryEntry struct {
	name string
	data []byte
	mode fs.FileMode
}

func (e *memoryEntry) Name() string               { return e.name }
func (e *memoryEntry) Size() int64                { return int64(len(e.data)) }
func (e *memoryEntry) Mode() fs.FileMode          { return e.mode }
func (e *memoryEntry) ModTime() time.Time         { return time.Time{} }
func (e *memoryEntry) IsDir() bool                { return e.mode.IsDir() }
func (e *memoryEntry) Sys() any                   { return nil }
func (e *memoryEntry) Type() fs.FileMode          { return e.mode.Type() }
func (e *memoryEntry) Info() (fs.FileInfo, error) { return e, nil }

type memoryFS map[string]*memoryEntry

func (m memoryFS) add(name string, data []byte, mode fs.FileMode) error {
	if previous := m[name]; previous != nil && previous.IsDir() != mode.IsDir() {
		return fmt.Errorf("conflicting archive path %q", name)
	}
	m[name] = &memoryEntry{name: path.Base(name), data: data, mode: mode}
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		if e := m[parent]; e != nil {
			if !e.IsDir() {
				return fmt.Errorf("archive parent %q is a file", parent)
			}
			break
		}
		m[parent] = &memoryEntry{name: path.Base(parent), mode: fs.ModeDir | 0755}
	}
	return nil
}

func (m memoryFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	e := m[name]
	if e == nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	file := &memoryFile{Reader: bytes.NewReader(e.data), entry: e}
	if e.IsDir() {
		for p, child := range m {
			if p != name && path.Dir(p) == name {
				file.children = append(file.children, child)
			}
		}
		sort.Slice(file.children, func(i, j int) bool { return file.children[i].Name() < file.children[j].Name() })
	}
	return file, nil
}

type memoryFile struct {
	*bytes.Reader
	entry    *memoryEntry
	children []fs.DirEntry
	offset   int
}

func (f *memoryFile) Stat() (fs.FileInfo, error) { return f.entry, nil }
func (f *memoryFile) Close() error               { return nil }
func (f *memoryFile) ReadDir(n int) ([]fs.DirEntry, error) {
	if !f.entry.IsDir() {
		return nil, fs.ErrInvalid
	}
	rest := f.children[f.offset:]
	if n > 0 && len(rest) == 0 {
		return nil, io.EOF
	}
	if n > 0 && len(rest) > n {
		rest = rest[:n]
	}
	f.offset += len(rest)
	return rest, nil
}
