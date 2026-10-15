package migrate_test

import (
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/stopyellingatme/spinup/internal/codemod"
	"github.com/stopyellingatme/spinup/internal/migrate"
	"github.com/stopyellingatme/spinup/internal/migrate/migratetest"
)

func TestExampleMigration(t *testing.T) {
	t.Parallel()
	edit := func(c *migrate.Ctx) ([]byte, bool, error) {
		src, err := os.ReadFile(filepath.Join(c.Root, "app.yaml"))
		if err != nil {
			return nil, false, err
		}
		return codemod.EditYAML(src, func(doc *yaml.Node) (bool, error) {
			node := codemod.YAMLGet(doc, "app", "name")
			if node == nil || node.Value != "old" {
				return false, nil
			}
			return true, codemod.YAMLSet(doc, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "new"}, "app", "name")
		})
	}
	m := migrate.Migration{ID: "0001-example", Since: "0.2.0", Stage: migrate.After,
		Plan: func(c *migrate.Ctx) ([]migrate.Change, error) {
			_, changed, err := edit(c)
			if err != nil {
				return nil, err
			}
			if !changed {
				return nil, nil
			}
			return []migrate.Change{{Path: "app.yaml", Desc: "Rename the app"}}, nil
		},
		Apply: func(c *migrate.Ctx) error {
			out, changed, err := edit(c)
			if err != nil {
				return err
			}
			if !changed {
				return nil
			}
			return os.WriteFile(filepath.Join(c.Root, "app.yaml"), out, 0o644)
		},
	}
	migratetest.Run(t, m, "testdata/rename/before", "testdata/rename/after")
}
