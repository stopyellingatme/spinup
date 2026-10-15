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
)

func init() {
	registry.Register(gen.Generator{Name: "client", Short: "Generate TypeScript types from OpenAPI", Run: plan})
	run.RegisterPostStep(gen.PostClient, postStep)
}

func plan(_ context.Context, in gen.Input) (*gen.Output, error) {
	if len(in.Args) != 0 {
		return nil, fmt.Errorf("cannot generate client: expected no positional arguments\nTry: spinup g client")
	}
	return &gen.Output{PostSteps: []gen.PostStep{gen.PostOpenAPI, gen.PostClient}, Next: []string{"cd web && pnpm check"}}, nil
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
	info, err := os.Stat(filepath.Join(root, "web/node_modules"))
	switch {
	case os.IsNotExist(err):
		if err := pnpm(ctx, root, r, "--dir", "web", "install", "--frozen-lockfile"); err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("inspect web/node_modules: %w\nTry: check the directory permissions", err)
	case !info.IsDir():
		return fmt.Errorf("cannot generate client: web/node_modules is not a directory\nTry: remove web/node_modules, then run spinup g client")
	}
	return GenerateTypes(ctx, root, "src/lib/api/schema.d.ts", r)
}

// GenerateTypes runs openapi-typescript from root and writes to output relative to web/.
// An absolute output path permits drift checks without changing the application schema.
func GenerateTypes(ctx context.Context, root, output string, r exec.Runner) error {
	return pnpm(ctx, root, r, "--dir", "web", "exec", "openapi-typescript", "../api/openapi/bundle.gen.yaml", "-o", output)
}

func pnpm(ctx context.Context, root string, r exec.Runner, args ...string) error {
	result, err := r.Run(ctx, exec.Cmd{Name: "pnpm", Args: args, Dir: root})
	if err != nil {
		return fmt.Errorf("run pnpm for client generation: %w\n%s%s\nTry: ensure pnpm is on PATH, correct the tool error, then run spinup g client", err, result.Stdout, result.Stderr)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("pnpm for client generation exited with code %d\n%s%s\nTry: correct the tool error, then run spinup g client", result.ExitCode, result.Stdout, result.Stderr)
	}
	return nil
}
