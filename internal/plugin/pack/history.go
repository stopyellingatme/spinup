package pack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/project"
)

const historyFile = ".spinup/plugins.history"

type historyEntry struct {
	Name, Version, Source, SHA256 string
}

func readHistory(root string) ([]historyEntry, error) {
	data, err := os.ReadFile(filepath.Join(root, historyFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, problem("read plugin history", err)
	}
	var entries []historyEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, problem("decode plugin history", err)
	}
	for _, entry := range entries {
		if !namePattern.MatchString(entry.Name) || !safeVersion(entry.Version) || !token(entry.Source) || !validHash(entry.SHA256) {
			return nil, problem("validate plugin history", fmt.Errorf("invalid record for %q", entry.Name))
		}
	}
	return entries, nil
}

func (m *Manager) historyHash(name, version, source string) (string, error) {
	entries, err := readHistory(m.Root)
	if err != nil {
		return "", err
	}
	var hash string
	for _, entry := range entries {
		if entry.Name != name || !sameVersion(entry.Version, version) || entry.Source != source {
			continue
		}
		if hash != "" && hash != entry.SHA256 {
			return "", problem("resolve historical plugin", fmt.Errorf("project history has multiple hashes for %s@%s", name, version))
		}
		hash = entry.SHA256
	}
	return hash, nil
}

func (m *Manager) historyBytes(cfg *project.Config, lock *manifest.Lock, allow map[string]approval, loaded *plugin.Loaded) ([]byte, error) {
	entries, err := readHistory(m.Root)
	if err != nil {
		return nil, err
	}
	// Preserve approvals made before the history file existed, using project records.
	for _, decl := range cfg.Plugins {
		for _, entry := range lock.Plugins {
			approved := allow[decl.Name]
			if entry.Name == decl.Name && approved.Source == decl.Source && approved.SHA256 == entry.SHA256 && validHash(entry.SHA256) {
				entries = append(entries, historyEntry{decl.Name, strings.TrimPrefix(entry.Version, "v"), decl.Source, entry.SHA256})
			}
		}
	}
	if loaded != nil {
		entries = append(entries, historyEntry{loaded.Meta.Name, strings.TrimPrefix(loaded.Meta.Version, "v"), loaded.Source, loaded.SHA256})
	}
	key := func(e historyEntry) string {
		return strings.Join([]string{e.Name, e.Version, e.Source, e.SHA256}, "\x00")
	}
	sort.Slice(entries, func(i, j int) bool { return key(entries[i]) < key(entries[j]) })
	unique := make([]historyEntry, 0, len(entries))
	for _, entry := range entries {
		if len(unique) == 0 || unique[len(unique)-1] != entry {
			unique = append(unique, entry)
		}
	}
	data, err := json.MarshalIndent(unique, "", "  ")
	if err != nil {
		return nil, problem("encode plugin history", err)
	}
	return append(data, '\n'), nil
}
