package openapi

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/gen/run"
	"go.yaml.in/yaml/v3"
)

func init() {
	registry.Register(gen.Generator{Name: "openapi", Short: "Bundle OpenAPI files and generate Go types", Example: `  # Bundle the OpenAPI files and regenerate the Go types
  spinup g openapi`, Run: plan})
	run.RegisterPostStep(gen.PostOpenAPI, postStep)
}

func plan(_ context.Context, in gen.Input) (*gen.Output, error) {
	if len(in.Args) != 0 {
		return nil, fmt.Errorf("cannot generate OpenAPI: expected no positional arguments\nTry: spinup g openapi")
	}
	return &gen.Output{PostSteps: []gen.PostStep{gen.PostOpenAPI, gen.PostServiceClients}, Next: []string{"cd api && go build ./..."}}, nil
}

func postStep(ctx context.Context, root string, _ []string, runner exec.Runner) error {
	return generate(ctx, filepath.Join(root, "api"), runner, run.PostStepOutput(ctx))
}

func generate(ctx context.Context, apiDir string, runner exec.Runner, notices io.Writer) error {
	data, err := Bundle(apiDir)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(apiDir, "openapi", "bundle.gen.yaml"), data, 0644); err != nil {
		return fmt.Errorf("write api/openapi/bundle.gen.yaml: %w\nTry: check the directory permissions, then run spinup g openapi", err)
	}
	empty, err := noPaths(data)
	if err != nil {
		return err
	}
	if empty {
		if _, err := fmt.Fprintln(notices, "no OpenAPI paths; oapi-codegen skipped\nTry: spinup g resource <name> <field:type>..."); err != nil {
			return fmt.Errorf("write OpenAPI notice: %w\nTry: check the output destination, then run spinup g openapi", err)
		}
		return nil
	}
	cmd := run.ToolCommand(ctx, apiDir, runner, "oapi-codegen", "-config", "oapi-codegen.yaml", "openapi/bundle.gen.yaml")
	cmd.Stdout, cmd.Stderr = notices, notices
	result, err := runner.Run(ctx, cmd)
	if err != nil {
		return fmt.Errorf("run oapi-codegen: %w\n%s%s\nTry: correct the tool error, then run spinup g openapi", err, result.Stdout, result.Stderr)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("oapi-codegen exited with code %d\n%s%s\nTry: correct the tool error, then run spinup g openapi", result.ExitCode, result.Stdout, result.Stderr)
	}
	return nil
}

func noPaths(data []byte) (bool, error) {
	var doc struct {
		Paths map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false, fmt.Errorf("read bundled paths: %w\nTry: correct api/openapi/, then run spinup g openapi", err)
	}
	return len(doc.Paths) == 0, nil
}
