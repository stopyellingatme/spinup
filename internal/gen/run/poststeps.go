package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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
			cmd := ToolCommand(ctx, filepath.Join(root, "api"), r, "sqlc", "generate")
			return tool(ctx, root, r, cmd.Name, cmd.Args...)
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
	for _, step := range []gen.PostStep{gen.PostOpenAPI, gen.PostSQLC, gen.PostClient, gen.PostServiceClients, gen.PostGofmt} {
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

type postStepOutputKey struct{}

// PostStepOutput returns the output buffer for the current post-step.
// Outside the generator runner, notices go to standard error.
func PostStepOutput(ctx context.Context) io.Writer {
	if out, ok := ctx.Value(postStepOutputKey{}).(io.Writer); ok {
		return out
	}
	return os.Stderr
}

func runPostSteps(ctx context.Context, root string, files []string, ordered []gen.PostStep, runners []postStepRunner, runner exec.Runner, out io.Writer) error {
	for start := 0; start < len(ordered); {
		end := start + 1
		// OpenAPI writes the bundle and API types. sqlc writes the store package.
		// Later steps can consume those files and must wait for both tools.
		if ordered[start] == gen.PostOpenAPI && end < len(ordered) && ordered[end] == gen.PostSQLC {
			end++
		}
		outputs := make([]bytes.Buffer, end-start)
		errs := make([]error, end-start)
		var wg sync.WaitGroup
		for i := start; i < end; i++ {
			stepCtx := context.WithValue(ctx, postStepOutputKey{}, &outputs[i-start])
			wg.Go(func() {
				errs[i-start] = runners[i](stepCtx, root, files, runner)
			})
		}
		wg.Wait()
		var failures []error
		for i := start; i < end; i++ {
			if _, err := outputs[i-start].WriteTo(out); err != nil {
				failures = append(failures, withHint("write generator post-step output", err, "check the output destination"))
			}
			if err := errs[i-start]; err != nil {
				failures = append(failures, fmt.Errorf("generator post-step %q failed: %w\nTry: %s", ordered[i], err, retryStep(ordered[i], files)))
			}
		}
		if err := errors.Join(failures...); err != nil {
			return err
		}
		start = end
	}
	return nil
}

func tool(ctx context.Context, root string, r exec.Runner, name string, args ...string) error {
	out := PostStepOutput(ctx)
	result, err := r.Run(ctx, exec.Cmd{Name: name, Args: args, Dir: filepath.Join(root, "api"), Stdout: out, Stderr: out})
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
	if step == gen.PostServiceClients {
		return "run spinup g client from the application root by hand"
	}
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
