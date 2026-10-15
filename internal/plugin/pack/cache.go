package pack

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

type receipt struct{ Source, SHA256 string }

func (m *Manager) cacheRoot() (string, error) {
	if m.CacheDir != "" {
		return m.CacheDir, nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", problem("find plugin cache", err)
	}
	return filepath.Join(dir, "spinup", "plugins"), nil
}
func (m *Manager) cacheDir(name, version, source, hash string) (string, error) {
	if !namePattern.MatchString(name) || !safeVersion(version) || !token(source) || !validHash(hash) {
		return "", problem("find plugin cache", fmt.Errorf("invalid cache identity for %q/%q", name, version))
	}
	root, err := m.cacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name, strings.TrimPrefix(version, "v"), fmt.Sprintf("%x", sha256.Sum256([]byte(source))), hash), nil
}
func (m *Manager) cached(name, version, source, hash string) (tree, error) {
	dir, err := m.cacheDir(name, version, source, hash)
	if err != nil {
		return tree{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "receipt.json"))
	if err != nil {
		return tree{}, problem("read plugin cache", fmt.Errorf("offline cache miss for %s@%s: %w", name, version, err))
	}
	var approved receipt
	if err := json.Unmarshal(data, &approved); err != nil {
		return tree{}, problem("read plugin receipt", err)
	}
	if approved.Source != source {
		return tree{}, problem("read plugin cache", fmt.Errorf("cached source differs for %q", name))
	}
	if approved.SHA256 != hash {
		return tree{}, problem("verify plugin cache", fmt.Errorf("receipt hash differs from project approval for %q", name))
	}
	tr, err := readTree(filepath.Join(dir, "tree"))
	if err != nil {
		return tree{}, err
	}
	if tr.hash() != hash {
		return tree{}, problem("verify plugin cache", fmt.Errorf("cached hash differs for %q", name))
	}
	return tr, nil
}
func (m *Manager) cache(t tree, loaded plugin.Loaded) error {
	dir, err := m.cacheDir(loaded.Meta.Name, loaded.Meta.Version, loaded.Source, loaded.SHA256)
	if err != nil {
		return err
	}
	// VFS checks every parent for symlinks and rolls back failed writes.
	data, err := json.Marshal(receipt{Source: loaded.Source, SHA256: loaded.SHA256})
	if err != nil {
		return problem("encode plugin receipt", err)
	}
	root, err := m.cacheRoot()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return problem("create plugin cache", err)
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return problem("locate plugin cache", err)
	}
	var plan vfs.Plan
	// Remove old tree files so a same-version update cannot retain stale data.
	existing, err := readTree(filepath.Join(dir, "tree"))
	if err == nil {
		newPaths := map[string]bool{}
		for _, file := range t.plan.Files {
			newPaths[file.Path] = true
		}
		for _, file := range existing.plan.Files {
			if !newPaths[file.Path] {
				if err := plan.Add(vfs.File{Path: filepath.ToSlash(filepath.Join(rel, "tree", file.Path)), Delete: true}); err != nil {
					return err
				}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, file := range t.plan.Files {
		file.Path = filepath.ToSlash(filepath.Join(rel, "tree", file.Path))
		if err := plan.Add(file); err != nil {
			return err
		}
	}
	if err := plan.Add(vfs.File{Path: filepath.ToSlash(filepath.Join(rel, "receipt.json")), Data: append(data, '\n'), Mode: 0644}); err != nil {
		return err
	}
	if _, err := vfs.Apply(root, plan, vfs.ApplyOptions{}); err != nil {
		return problem("save plugin cache", err)
	}
	return nil
}
