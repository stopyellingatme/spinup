package manifest

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"sync"
)

var (
	migrationMu sync.RWMutex
	migrations  = make(map[int]func(map[string]any) error)
)

// RegisterMigration registers a migration from schema from to schema from+1.
// Registration replaces any previous function for that schema.
// Parse advances the schema after a successful migration.
func RegisterMigration(from int, fn func(raw map[string]any) error) {
	migrationMu.Lock()
	defer migrationMu.Unlock()
	migrations[from] = fn
}

// Parse decodes, migrates, and validates a JSON lock.
// Numbers in Answers and migration inputs use json.Number to preserve precision.
func Parse(data []byte) (*Lock, error) {
	var raw map[string]any
	if err := decodeJSON(data, &raw); err != nil {
		return nil, fmt.Errorf("parse spinup.lock: invalid JSON: %w\nTry: repair the JSON in spinup.lock", err)
	}
	version, err := rawSchema(raw)
	if err != nil {
		return nil, err
	}
	if version > currentSchemaVersion {
		return nil, fmt.Errorf("parse spinup.lock: schema %d is newer than supported schema %d\nTry: spinup self-update", version, currentSchemaVersion)
	}
	for version < currentSchemaVersion {
		migrationMu.RLock()
		migrate := migrations[version]
		migrationMu.RUnlock()
		if migrate == nil {
			return nil, fmt.Errorf("parse spinup.lock: migration from schema %d to %d is unavailable\nTry: spinup self-update", version, version+1)
		}
		if err := migrate(raw); err != nil {
			return nil, fmt.Errorf("parse spinup.lock: migration from schema %d to %d failed: %w\nTry: repair spinup.lock before retrying the migration", version, version+1, err)
		}
		version++
		raw["schema"] = version
	}
	data, err = json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("parse spinup.lock: migrated data is not JSON: %w\nTry: repair the schema migration", err)
	}
	lock := New()
	if err := decodeJSON(data, lock); err != nil {
		return nil, fmt.Errorf("parse spinup.lock: invalid field type: %w\nTry: repair the fields in spinup.lock", err)
	}
	ensureMaps(lock)
	if err := lock.Validate(); err != nil {
		return nil, err
	}
	return lock, nil
}

func rawSchema(raw map[string]any) (int, error) {
	number, ok := raw["schema"].(json.Number)
	if ok {
		version, err := strconv.Atoi(string(number))
		if err == nil && version >= 1 {
			return version, nil
		}
	}
	return 0, fmt.Errorf("parse spinup.lock: schema must be an integer of at least 1\nTry: restore a valid spinup.lock")
}

// Marshal returns canonical JSON with sorted keys, ordered collections, and a trailing newline.
// It does not modify the lock.
func (l *Lock) Marshal() ([]byte, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	canonical := *l
	ensureMaps(&canonical)
	canonical.Tombstones = sortedUnique(l.Tombstones)
	canonical.Orphans = sortedUnique(l.Orphans)
	canonical.AppliedMigrations = sortedUnique(l.AppliedMigrations)
	canonical.Plugins = append([]Plugin{}, l.Plugins...)
	slices.SortFunc(canonical.Plugins, comparePlugins)
	// Convert structs to maps so encoding/json sorts every object's keys.
	data, err := json.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("marshal spinup.lock: a field cannot be encoded as JSON: %w\nTry: use JSON-compatible answer values", err)
	}
	var raw map[string]any
	if err := decodeJSON(data, &raw); err != nil {
		return nil, fmt.Errorf("marshal spinup.lock: a field produced invalid JSON: %w\nTry: use JSON-compatible answer values", err)
	}
	data, err = json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal spinup.lock: cannot encode canonical JSON: %w\nTry: use JSON-compatible answer values", err)
	}
	return append(data, '\n'), nil
}

func decodeJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

func ensureMaps(l *Lock) {
	if l.Answers == nil {
		l.Answers = make(map[string]any)
	}
	if l.StackVersions == nil {
		l.StackVersions = make(map[string]string)
	}
	if l.Files == nil {
		l.Files = make(map[string]FileEntry)
	}
}

func comparePlugins(a, b Plugin) int {
	// Secondary fields make duplicate-name plugin ordering deterministic too.
	return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Version, b.Version),
		cmp.Compare(a.SHA256, b.SHA256), cmp.Compare(a.API, b.API))
}
