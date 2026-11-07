package billing

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
	registry.Register(gen.Generator{Name: "billing", Short: "Generate the app billing configuration", Example: `  # Rewrite the billing files from the [billing] table of spinup.toml
  spinup g billing`, Run: plan})
}

func plan(_ context.Context, in gen.Input) (*gen.Output, error) {
	if len(in.Args) != 0 {
		return nil, fmt.Errorf("cannot generate billing: expected no positional arguments\nTry: spinup g billing")
	}
	if in.Lock == nil || !slices.Contains(in.Lock.Packs, "billing") {
		return nil, fmt.Errorf("cannot generate billing: the app has no billing module\nTry: spinup module add billing")
	}
	config, err := project.Load(in.Root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("cannot generate billing: spinup.toml is missing\nTry: run this inside a spinup project")
		}
		if strings.Contains(err.Error(), "\nTry:") {
			return nil, fmt.Errorf("cannot generate billing: %w", err)
		}
		return nil, fmt.Errorf("cannot generate billing: %w\nTry: run this inside a spinup project", err)
	}
	decls := config.BillingConfig()
	if err := decls.Validate(config.BillingContext()); err != nil {
		return nil, err
	}
	return &gen.Output{Plan: vfs.Plan{Files: []vfs.File{
		{Path: project.BillingFilePath, Data: project.RenderBillingFile(decls), Class: string(manifest.Managed)},
		{Path: project.BillingTSFilePath, Data: project.RenderBillingTSFile(decls), Class: string(manifest.Managed)},
	}}, Edits: []string{project.BillingFilePath, project.BillingTSFilePath}}, nil
}
