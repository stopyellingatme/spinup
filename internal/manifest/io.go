package manifest

import (
	"fmt"
	"os"
	"path/filepath"
)

// Load reads, migrates, and validates the lock at path.
func Load(path string) (*Lock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load spinup.lock at %q: %w\nTry: check the lock path and file permissions", path, err)
	}
	return Parse(data)
}

// Save atomically replaces path with canonical JSON through a temporary file.
// The temporary file is created in the destination directory.
func (l *Lock) Save(path string) error {
	data, err := l.Marshal()
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".spinup.lock-*")
	if err != nil {
		return saveError(path, err)
	}
	defer func() { _ = os.Remove(file.Name()) }()
	defer func() { _ = file.Close() }()
	if err := file.Chmod(0644); err != nil {
		return saveError(path, err)
	}
	if _, err := file.Write(data); err != nil {
		return saveError(path, err)
	}
	if err := file.Sync(); err != nil {
		return saveError(path, err)
	}
	if err := file.Close(); err != nil {
		return saveError(path, err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return saveError(path, err)
	}
	return nil
}

func saveError(path string, err error) error {
	return fmt.Errorf("save spinup.lock at %q: %w\nTry: check the destination directory and file permissions", path, err)
}
