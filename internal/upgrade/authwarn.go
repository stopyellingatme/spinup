package upgrade

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/stopyellingatme/spinup/internal/modules"
	"github.com/stopyellingatme/spinup/internal/project"
)

// authUpgradeWarnings annotates new auth files without changing their actions.
// Reasons appear in both the printed plan and the JSON report.
func authUpgradeWarnings(root *os.Root, plan *Plan) error {
	for i := range plan.Files {
		file := &plan.Files[i]
		if file.Action != ActionCreate {
			continue
		}
		switch file.Path {
		case "api/db/migrations/00005_users_role.sql":
			entries, err := fs.ReadDir(root.FS(), "api/db/migrations")
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("inspect auth migration versions: %w\nTry: check permissions on api/db/migrations", err)
			}
			var names []string
			for _, entry := range entries {
				name := entry.Name()
				if !entry.IsDir() && name != "00005_users_role.sql" && strings.HasPrefix(name, "00005_") && strings.HasSuffix(name, ".sql") {
					names = append(names, name)
				}
			}
			if len(names) > 0 {
				file.Reason = "Warning: migration version 5 is also used by " + strings.Join(names, ", ") + ".\nPending migration: Try: rename your migration to the next free number, then run spinup upgrade again.\nApplied migration: keep version 5. Try: rename 00005_users_role.sql to the next free number before you run spinup db migrate."
			}
		case project.RolesFilePath:
			data, err := root.ReadFile(project.ConfigFile)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return fmt.Errorf("read auth upgrade configuration: %w\nTry: check permissions on spinup.toml", err)
			}
			var config map[string]any
			if err := toml.Unmarshal(data, &config); err != nil {
				return fmt.Errorf("read auth upgrade configuration: %w\nTry: correct spinup.toml and run spinup upgrade again", err)
			}
			if _, ok := config["auth"].(map[string]any); ok {
				file.Reason = "Note: spinup.toml declares [auth], but roles.gen.go is absent. The upgrade creates the default role set.\nTry: run spinup g roles after the upgrade."
			}
		}
	}
	return nil
}

// migrationClashWarnings annotates each new pack migration whose version an app
// migration already uses (ADR 0025 decision 2.6). The kit migrator rejects two files
// with one version. A file that already has a reason keeps it.
func migrationClashWarnings(root *os.Root, plan *Plan) error {
	var added []string
	for _, file := range plan.Files {
		if file.Action == ActionCreate && file.Reason == "" && strings.HasPrefix(file.Path, "api/db/migrations/") {
			added = append(added, file.Path)
		}
	}
	if len(added) == 0 {
		return nil
	}
	warnings, err := modules.MigrationClashes(root.FS(), added)
	if err != nil {
		return err
	}
	for i := range plan.Files {
		if warning, ok := warnings[plan.Files[i].Path]; ok && plan.Files[i].Reason == "" {
			plan.Files[i].Reason = warning
		}
	}
	return nil
}
