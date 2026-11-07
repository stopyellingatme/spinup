package upgrade

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/pelletier/go-toml/v2"

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
		if file.Path == project.RolesFilePath {
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
