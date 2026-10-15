package manifest

import (
	"fmt"
	"slices"
)

// Schema 2 adds "packs": the template packs of the app in render order (ADR 0006).
// A schema 1 lock comes from an M1 build, where every app used base and api plus the
// packs named by the modules answer.
func init() {
	RegisterMigration(1, migrateV1Packs)
}

func migrateV1Packs(raw map[string]any) error {
	if _, ok := raw["packs"]; ok {
		return nil
	}
	packs := []any{"base", "api"}
	answers, _ := raw["answers"].(map[string]any)
	if modules, ok := answers["modules"].([]any); ok {
		for _, module := range modules {
			name, ok := module.(string)
			if !ok {
				return fmt.Errorf("answers.modules holds %T, want strings", module)
			}
			if name != "" && !slices.Contains(packs, any(name)) && name != "web" {
				packs = append(packs, name)
			}
		}
	}
	raw["packs"] = packs
	return nil
}
