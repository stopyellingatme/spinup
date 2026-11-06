package flags

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
	registry.Register(gen.Generator{Name: "flags", Short: "Generate the app flag set", Example: `  # Rewrite api/internal/flags/flags.gen.go from the [flags] tables of spinup.toml
  spinup g flags`, Run: plan})
}

func plan(_ context.Context, in gen.Input) (*gen.Output, error) {
	if len(in.Args) != 0 {
		return nil, fmt.Errorf("cannot generate flags: expected no positional arguments\nTry: spinup g flags")
	}
	if in.Lock == nil || !slices.Contains(in.Lock.Packs, "flags") {
		return nil, fmt.Errorf("cannot generate flags: the app has no flags module\nTry: spinup module add flags")
	}
	config, err := project.Load(in.Root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("cannot generate flags: spinup.toml is missing\nTry: run this inside a spinup project")
		}
		if strings.Contains(err.Error(), "\nTry:") {
			return nil, fmt.Errorf("cannot generate flags: %w", err)
		}
		return nil, fmt.Errorf("cannot generate flags: %w\nTry: run this inside a spinup project", err)
	}
	decls := config.FlagDecls()
	if err := decls.Validate(); err != nil {
		return nil, err
	}
	roles := config.AuthRoles()
	if err := roles.Validate(); err != nil {
		return nil, err
	}
	return &gen.Output{Plan: vfs.Plan{Files: []vfs.File{
		{Path: project.FlagsFilePath, Data: project.RenderFlagsFile(decls), Class: string(manifest.Managed)},
		{Path: project.FlagsTSFilePath, Data: project.RenderFlagsTSFile(decls, roles), Class: string(manifest.Managed)},
	}}, Edits: []string{project.FlagsFilePath, project.FlagsTSFilePath}}, nil
}
