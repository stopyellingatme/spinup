package testutil

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rogpeppe/go-internal/diff"
)

type entry struct {
	data []byte
	mode fs.FileMode
}
type tree map[string]entry

func readTree(source fs.FS) (tree, error) {
	files := tree{}
	err := fs.WalkDir(source, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: unsupported file mode %s", name, info.Mode())
		}
		data, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		files[name] = entry{data: data, mode: info.Mode().Perm()}
		return nil
	})
	return files, err
}

func readGolden(dir string) (tree, error) {
	files, err := readTree(os.DirFS(dir))
	if err != nil {
		return nil, err
	}
	modes := files[".modes"].data
	delete(files, ".modes")
	// The manifest is authoritative, regardless of the checkout's permissions.
	for name, file := range files {
		file.mode = 0644
		files[name] = file
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(modes), "\n"), "\n") {
		if line == "" {
			continue
		}
		octal, name, ok := strings.Cut(line, " ")
		mode, err := strconv.ParseUint(octal, 8, 32)
		file, exists := files[name]
		if !ok || err != nil || mode > 0777 || !exists || seen[name] {
			return nil, fmt.Errorf("invalid .modes entry %q", line)
		}
		seen[name] = true
		file.mode = fs.FileMode(mode)
		files[name] = file
	}
	return files, nil
}

func sortedPaths(files tree) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func writeGolden(dir string, files tree) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	var modes strings.Builder
	for _, name := range sortedPaths(files) {
		file := files[name]
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(target, file.data, file.mode); err != nil {
			return err
		}
		if err := os.Chmod(target, file.mode); err != nil {
			return err
		}
		if file.mode != 0644 {
			fmt.Fprintf(&modes, "%04o %s\n", file.mode, name)
		}
	}
	return os.WriteFile(filepath.Join(dir, ".modes"), []byte(modes.String()), 0644)
}

func compareTrees(want, got tree) []string {
	union := tree{}
	for name := range want {
		union[name] = entry{}
	}
	for name := range got {
		union[name] = entry{}
	}
	var reports []string
	for _, name := range sortedPaths(union) {
		old, existed := want[name]
		newFile, exists := got[name]
		var changes []string
		switch {
		case !existed:
			changes = append(changes, "added: "+name)
		case !exists:
			changes = append(changes, "missing: "+name)
		default:
			if old.mode != newFile.mode {
				changes = append(changes, fmt.Sprintf("mode: %s: want %04o, got %04o", name, old.mode, newFile.mode))
			}
			if !bytes.Equal(old.data, newFile.data) {
				changes = append(changes, "changed: "+name)
			}
		}
		if !bytes.Equal(old.data, newFile.data) {
			if utf8.Valid(old.data) && utf8.Valid(newFile.data) && !bytes.ContainsRune(old.data, 0) && !bytes.ContainsRune(newFile.data, 0) {
				lines := strings.Split(strings.TrimSuffix(string(diff.Diff("want/"+name, old.data, "got/"+name, newFile.data)), "\n"), "\n")
				if len(lines) > 40 {
					lines = append(lines[:40], "... diff truncated after 40 lines")
				}
				changes = append(changes, strings.Join(lines, "\n"))
			} else {
				changes = append(changes, "binary content differs")
			}
		}
		if len(changes) != 0 {
			reports = append(reports, strings.Join(changes, "\n"))
		}
	}
	return reports
}
