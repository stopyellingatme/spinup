package apikeys

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

func init() {
	registry.Register(gen.Generator{Name: "apikeys", Short: "Generate the app API key configuration", Example: `  # Rewrite api/internal/apikeys/apikeys.gen.go from the [apikeys] table of spinup.toml
  spinup g apikeys`, Run: plan})
}

func plan(_ context.Context, in gen.Input) (*gen.Output, error) {
	if len(in.Args) != 0 {
		return nil, fmt.Errorf("cannot generate apikeys: expected no positional arguments\nTry: spinup g apikeys")
	}
	if in.Lock == nil || !slices.Contains(in.Lock.Packs, "apikeys") {
		return nil, fmt.Errorf("cannot generate apikeys: the app has no apikeys module\nTry: spinup module add apikeys")
	}
	config, err := project.Load(in.Root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("cannot generate apikeys: spinup.toml is missing\nTry: run this inside a spinup project")
		}
		if strings.Contains(err.Error(), "\nTry:") {
			return nil, fmt.Errorf("cannot generate apikeys: %w", err)
		}
		return nil, fmt.Errorf("cannot generate apikeys: %w\nTry: run this inside a spinup project", err)
	}
	decls := config.APIKeysConfig()
	if err := decls.Validate(); err != nil {
		return nil, err
	}
	roles := config.AuthRoles()
	if err := roles.Validate(); err != nil {
		return nil, err
	}
	return &gen.Output{Plan: vfs.Plan{Files: []vfs.File{
		{Path: project.APIKeysFilePath, Data: project.RenderAPIKeysFile(decls), Class: string(manifest.Managed)},
		{Path: project.APIKeysTSFilePath, Data: project.RenderAPIKeysTSFile(decls, roles), Class: string(manifest.Managed)},
	}}, Edits: []string{project.APIKeysFilePath, project.APIKeysTSFilePath}}, nil
}
