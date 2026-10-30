package client

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/gen/run"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/webtool"
)

func init() {
	registry.Register(gen.Generator{Name: "client", Short: "Generate TypeScript types from OpenAPI", Run: plan})
	run.RegisterPostStep(gen.PostClient, postStep)
}

func plan(_ context.Context, in gen.Input) (*gen.Output, error) {
	if len(in.Args) != 0 {
		return nil, fmt.Errorf("cannot generate client: expected no positional arguments\nTry: spinup g client")
	}
	return &gen.Output{PostSteps: []gen.PostStep{gen.PostOpenAPI, gen.PostClient, gen.PostServiceClients}, Next: []string{"cd web && " + webtool.FromLock(in.Lock).Run("check").String()}}, nil
}

func postStep(ctx context.Context, root string, _ []string, r exec.Runner) error {
	// Keep notices on stderr because the generator can emit JSON on stdout.
	return generate(ctx, root, r, os.Stderr)
}

func generate(ctx context.Context, root string, r exec.Runner, notices io.Writer) error {
	lock, err := manifest.Load(filepath.Join(root, "spinup.lock"))
	if err != nil {
		return err
	}
	reason := ""
	if !slices.Contains(lock.Packs, "web") {
		reason = "no web pack"
	} else {
		if _, err := os.Stat(filepath.Join(root, "api/openapi/bundle.gen.yaml")); os.IsNotExist(err) {
			reason = "api/openapi/bundle.gen.yaml is missing"
		} else if err != nil {
			return fmt.Errorf("inspect OpenAPI bundle: %w\nTry: check api/openapi/bundle.gen.yaml permissions", err)
		}
	}
	if reason != "" {
		_, err := fmt.Fprintln(notices, "client generation skipped: "+reason)
		if err != nil {
			return fmt.Errorf("write client notice: %w\nTry: check the output destination", err)
		}
		return nil
	}
	tool := webtool.FromLock(lock)
	info, err := os.Stat(filepath.Join(root, "web/node_modules"))
	switch {
	case os.IsNotExist(err):
		if err := command(ctx, r, tool.InWeb(root, tool.Install(true, false))); err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("inspect web/node_modules: %w\nTry: check the directory permissions", err)
	case !info.IsDir():
		return fmt.Errorf("cannot generate client: web/node_modules is not a directory\nTry: remove web/node_modules, then run spinup g client")
	}
	return GenerateTypes(ctx, root, tool.ClientOutput(), r)
}

// GenerateTypes runs the selected client generator with output relative to web/.
// An absolute output path permits drift checks without changing application files.
// Heyapi also generates schema.d.ts beside its output for resource page types.
func GenerateTypes(ctx context.Context, root, output string, r exec.Runner) error {
	lock, err := manifest.Load(filepath.Join(root, "spinup.lock"))
	if err != nil {
		return err
	}
	tool := webtool.FromLock(lock)
	if err := command(ctx, r, tool.InWeb(root, tool.Client(output))); err != nil {
		return err
	}
	if tool.HeyAPI() {
		return command(ctx, r, tool.InWeb(root, tool.Schema(filepath.ToSlash(filepath.Join(filepath.Dir(output), "schema.d.ts")))))
	}
	return nil
}

func command(ctx context.Context, r exec.Runner, cmd exec.Cmd) error {
	result, err := r.Run(ctx, cmd)
	if err != nil {
		return fmt.Errorf("run %s for client generation: %w\n%s%s\nTry: ensure %s is on PATH, correct the tool error, then run spinup g client", cmd.Name, err, result.Stdout, result.Stderr, cmd.Name)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%s for client generation exited with code %d\n%s%s\nTry: correct the tool error, then run spinup g client", cmd.Name, result.ExitCode, result.Stdout, result.Stderr)
	}
	return nil
}
