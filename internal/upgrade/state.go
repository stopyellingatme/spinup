package upgrade

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/ui"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// runState is stored only in the main checkout. Status names the next operation.
type runState struct {
	Schema             int            `json:"schema"`
	Chain              []string       `json:"chain"`
	Index              int            `json:"step"`
	Worktree           string         `json:"worktree"`
	Branch             string         `json:"branch"`
	Status             string         `json:"status"`
	Conflicts          []string       `json:"conflicts,omitempty"`
	CommittedConflicts []string       `json:"committed_conflicts,omitempty"`
	Lock               *manifest.Lock `json:"lock"`
	Plan               *Plan          `json:"plan,omitempty"`
	Options            Options        `json:"options"`
}

func statePath(root string) string                 { return filepath.Join(root, ".spinup", "upgrade.json") }
func saveState(root string, state *runState) error { return writeUpgradeJSON(statePath(root), state) }

func loadState(root string) (*runState, error) {
	data, err := os.ReadFile(statePath(root))
	if err != nil {
		return nil, fmt.Errorf("load upgrade state: %w\nTry: start spinup upgrade", err)
	}
	var state runState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("load upgrade state: %w\nTry: restore .spinup/upgrade.json from a valid copy", err)
	}
	if err := state.validate(root); err != nil {
		return nil, err
	}
	return &state, nil
}

func (s *runState) validate(root string) error {
	invalid := func() error {
		return fmt.Errorf("load upgrade state: invalid or unsafe state\nTry: restore .spinup/upgrade.json from a valid copy")
	}
	if s.Schema != 1 || len(s.Chain) == 0 || s.Index < 0 || s.Index >= len(s.Chain) || s.Lock == nil {
		return invalid()
	}
	for _, v := range s.Chain {
		if _, err := chainVersion(v); err != nil {
			return invalid()
		}
	}
	to := s.Chain[len(s.Chain)-1]
	if s.Worktree != filepath.Join(root, ".spinup", "upgrade", to) || s.Branch != "spinup/upgrade-"+to {
		return invalid()
	}
	switch s.Status {
	case "step", "conflicts", "gofix", "after", "after-conflicts", "lock", "verify":
	default:
		return invalid()
	}
	if err := s.Lock.Validate(); err != nil {
		return err
	}
	for _, name := range append(slices.Clone(s.Conflicts), s.CommittedConflicts...) {
		clean, err := vfs.CleanPath(name)
		if err != nil || clean != name {
			return invalid()
		}
	}
	if s.Status != "step" && (s.Plan == nil || s.Plan.To != s.Chain[s.Index]) {
		return invalid()
	}
	return nil
}

func writeUpgradeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode upgrade state: %w\nTry: use valid upgrade metadata", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create upgrade storage: %w\nTry: check .spinup/ permissions", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".upgrade-*")
	if err != nil {
		return fmt.Errorf("create upgrade state: %w\nTry: check .spinup/ permissions", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(append(data, '\n'))
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), path)
	}
	if err != nil {
		return fmt.Errorf("save upgrade state: %w\nTry: check .spinup/ permissions", err)
	}
	return nil
}

func unresolved(work string, paths []string) ([]string, error) {
	root, err := os.OpenRoot(work)
	if err != nil {
		return nil, fmt.Errorf("open conflict files: %w\nTry: restore the upgrade worktree", err)
	}
	defer root.Close()
	var result []string
	for _, name := range paths {
		data, err := root.ReadFile(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read conflict %q: %w\nTry: restore and resolve the file in %s", name, err, work)
		}
		open := false
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			if bytes.HasPrefix(line, []byte("<<<<<<<")) {
				open = true
			}
			if open && bytes.HasPrefix(line, []byte(">>>>>>>")) {
				result = append(result, name)
				break
			}
		}
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}

func conflictError(work string, paths []string) error {
	return &ui.Error{What: "Upgrade has conflicts", Why: strings.Join(paths, ", "), Try: "resolve the markers in " + work + ", then run spinup upgrade --continue", Code: ui.ExitConflicts}
}
