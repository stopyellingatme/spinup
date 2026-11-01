package roles

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

func init() {
	registry.Register(gen.Generator{Name: "roles", Short: "Generate the app role set", Example: `  # Rewrite api/internal/roles/roles.gen.go from the [auth] table of spinup.toml
  spinup g roles`, Run: plan})
}

func plan(_ context.Context, in gen.Input) (*gen.Output, error) {
	if len(in.Args) != 0 {
		return nil, fmt.Errorf("cannot generate roles: expected no positional arguments\nTry: spinup g roles")
	}
	if in.Lock == nil || !slices.Contains(in.Lock.Packs, "auth") {
		return nil, fmt.Errorf("cannot generate roles: the app has no auth module\nTry: spinup add auth")
	}
	config, err := project.Load(in.Root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("cannot generate roles: spinup.toml is missing\nTry: run this inside a spinup project")
		}
		return nil, fmt.Errorf("cannot generate roles: %w\nTry: run this inside a spinup project", err)
	}
	roleSet := config.AuthRoles()
	if err := roleSet.Validate(); err != nil {
		return nil, err
	}
	return &gen.Output{
		Plan: vfs.Plan{Files: []vfs.File{{
			Path:  project.RolesFilePath,
			Data:  project.RenderRolesFile(roleSet),
			Class: string(manifest.Managed),
		}}},
		Edits: []string{project.RolesFilePath},
	}, nil
}
