package migration

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/stopyellingatme/spinup/internal/db/ops"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/ui"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

func init() {
	registry.Register(gen.Generator{
		Name:  "migration",
		Short: "Generate an empty SQL migration",
		Usage: "<snake_name>",
		Run:   run,
	})
}

// skeleton is the content of a new migration. It matches `spinup db new`.
const skeleton = `-- +goose Up
-- Write the SQL for this migration here.

-- +goose Down
-- Write the SQL that reverses the Up section here.
`

func run(_ context.Context, in gen.Input) (*gen.Output, error) {
	if len(in.Args) != 1 {
		return nil, fmt.Errorf("expected one migration name\nTry: spinup g migration add_index_to_todos")
	}
	name, err := ops.NextMigrationName(filepath.Join(in.Root, filepath.FromSlash(ops.MigrationsDir)), in.Args[0])
	if err != nil {
		var ue *ui.Error
		if errors.As(err, &ue) && ue.Try != "" {
			return nil, fmt.Errorf("%w\nTry: %s", err, ue.Try)
		}
		return nil, err
	}
	path := ops.MigrationsDir + "/" + name
	var plan vfs.Plan
	if err := plan.Add(vfs.File{Path: path, Data: []byte(skeleton), Class: string(manifest.Seed)}); err != nil {
		return nil, err
	}
	return &gen.Output{Plan: plan, Next: []string{"edit " + path, "spinup db migrate up"}}, nil
}
