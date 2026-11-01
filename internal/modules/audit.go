package modules

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
)

const migrationsDir = "api/db/migrations"

// MigrationClashes warns about duplicate versions in migrations added by packs.
// The filesystem contains the app files. The result maps each added path to its warning.
func MigrationClashes(disk fs.FS, additions []string) (map[string]string, error) {
	warnings := make(map[string]string)
	entries, err := fs.ReadDir(disk, migrationsDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("inspect pack migration versions: %w\nTry: check permissions on api/db/migrations", err)
	}
	for _, added := range additions {
		if path.Dir(added) != migrationsDir {
			continue
		}
		version, ok := migrationVersion(path.Base(added))
		if !ok {
			continue
		}
		var collisions []string
		for _, entry := range entries {
			existing, valid := migrationVersion(entry.Name())
			if !entry.IsDir() && valid && existing == version && entry.Name() != path.Base(added) {
				collisions = append(collisions, entry.Name())
			}
		}
		if len(collisions) > 0 {
			warnings[added] = fmt.Sprintf("Warning: %s uses migration version %d, also used by %s.\nTry: rename the app migration to the next free number before applying the pack migration. If the app migration is already applied, keep its version and rename %s instead.", added, version, strings.Join(collisions, ", "), path.Base(added))
		}
	}
	return warnings, nil
}

func migrationVersion(name string) (uint64, bool) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok || !strings.HasSuffix(name, ".sql") {
		return 0, false
	}
	version, err := strconv.ParseUint(prefix, 10, 64)
	return version, err == nil
}

// AuditNextSteps returns the manual wiring steps for an existing app with audit.
func AuditNextSteps() string {
	return `Next: in api/cmd/api/main.go, import:
 "github.com/stopyellingatme/spinup/kit/audit"
 "github.com/stopyellingatme/spinup/kit/audit/authsink"
Next: add these options to auth.New(pool, ...):
 auth.WithEvents(authsink.New(pool)),
 auth.WithActorContext(authsink.ActorContext),
Next: add this option to app.New(...):
 app.WithCommand("audit", audit.Command(pool)),
Next: run spinup db migrate up before starting the new binary.
Next: run spinup g audit <resource> for each existing table.
Next: wrap existing writes in audit.InTx and use s.Queries.WithTx(tx).
Next: run spinup doctor and inspect the audit-wired check.
`
}
