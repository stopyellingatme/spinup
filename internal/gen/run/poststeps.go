package run

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
)

type postStepRunner = func(context.Context, string, []string, exec.Runner) error

var (
	stepsMu sync.RWMutex
	steps   = map[gen.PostStep]postStepRunner{
		gen.PostSQLC: func(ctx context.Context, root string, _ []string, r exec.Runner) error {
			return tool(ctx, root, r, "go", "tool", "-modfile=tools.mod", "sqlc", "generate")
		},
		gen.PostGofmt: func(ctx context.Context, root string, files []string, r exec.Runner) error {
			args := gofmtArgs(files)
			if len(args) == 1 {
				return nil
			}
			return tool(ctx, root, r, "gofmt", args...)
		},
	}
)

// RegisterPostStep registers a tool runner. It panics for empty, nil, or duplicate registrations.
// The runner receives the project root and sorted paths relative to that root.
func RegisterPostStep(step gen.PostStep, fn func(ctx context.Context, root string, files []string, r exec.Runner) error) {
	stepsMu.Lock()
	defer stepsMu.Unlock()
	if step == "" || fn == nil || steps[step] != nil {
		panic(fmt.Sprintf("invalid or duplicate post-step %q", step))
	}
	steps[step] = fn
}

func orderedSteps(requested []gen.PostStep) []gen.PostStep {
	seen := make(map[gen.PostStep]bool)
	for _, step := range requested {
		seen[step] = true
	}
	ordered := make([]gen.PostStep, 0, len(seen))
	for _, step := range []gen.PostStep{gen.PostOpenAPI, gen.PostSQLC, gen.PostClient, gen.PostGofmt} {
		if seen[step] {
			ordered = append(ordered, step)
			delete(seen, step)
		}
	}
	unknown := make([]string, 0, len(seen))
	for step := range seen {
		unknown = append(unknown, string(step))
	}
	sort.Strings(unknown)
	for _, step := range unknown {
		ordered = append(ordered, gen.PostStep(step))
	}
	return ordered
}

func stepRunners(ordered []gen.PostStep) ([]postStepRunner, error) {
	stepsMu.RLock()
	defer stepsMu.RUnlock()
	runners := make([]postStepRunner, 0, len(ordered))
	for _, step := range ordered {
		fn := steps[step]
		if fn == nil {
			return nil, fmt.Errorf("cannot run generator post-step %q: no runner is registered\nTry: register the %s post-step runner, then repeat the generator", step, step)
		}
		runners = append(runners, fn)
	}
	return runners, nil
}

func tool(ctx context.Context, root string, r exec.Runner, name string, args ...string) error {
	result, err := r.Run(ctx, exec.Cmd{Name: name, Args: args, Dir: filepath.Join(root, "api")})
	if err != nil {
		return fmt.Errorf("%s: %w\n%s%s", name, err, result.Stdout, result.Stderr)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%s exited with code %d\n%s%s", name, result.ExitCode, result.Stdout, result.Stderr)
	}
	return nil
}

func gofmtArgs(files []string) []string {
	args := []string{"-w"}
	for _, file := range files {
		if strings.HasSuffix(file, ".go") {
			// Tools run in api/, while plan paths are relative to the project root.
			relative, err := filepath.Rel("api", filepath.FromSlash(file))
			if err == nil {
				args = append(args, relative)
			}
		}
	}
	return args
}

func retryStep(step gen.PostStep, files []string) string {
	command := "spinup g openapi"
	switch step {
	case gen.PostSQLC:
		command = "go tool -modfile=tools.mod sqlc generate"
	case gen.PostGofmt:
		command = (exec.Cmd{Name: "gofmt", Args: gofmtArgs(files)}).String()
	case gen.PostOpenAPI:
		command = "go tool -modfile=tools.mod oapi-codegen -config oapi-codegen.yaml openapi/bundle.gen.yaml"
	}
	return "run " + command + " in api/ by hand"
}
