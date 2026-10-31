package ci

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	spexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/services"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// MatrixEntry identifies a service and its detected language.
type MatrixEntry struct {
	// Name is the name declared in spinup.toml.
	Name string `json:"name"`
	// Lang is rust, python, node, or none.
	Lang string `json:"lang"`
}

// MatrixDoc describes the services for a GitHub Actions matrix.
type MatrixDoc struct {
	// Include follows the declaration order in spinup.toml. An empty list encodes as [].
	Include []MatrixEntry `json:"include"`
}

// serviceLanguages defines detection precedence and the required manifest files.
// Add each language here so detection remains consistent for every caller.
var serviceLanguages = []struct {
	name  string
	files []string
}{
	{"rust", []string{"Cargo.toml"}},
	{"python", []string{"pyproject.toml", "uv.lock"}},
	{"node", []string{"package.json"}},
}

// DetectLang returns rust, python, node, or none from the manifests in dir.
// Missing manifests and directories do not identify a language.
func DetectLang(dir string) string {
	for _, language := range serviceLanguages {
		match := true
		for _, file := range language.files {
			info, err := os.Stat(filepath.Join(dir, file))
			if err != nil || !info.Mode().IsRegular() {
				match = false
				break
			}
		}
		if match {
			return language.name
		}
	}
	return "none"
}

// Matrix loads the declared services and detects their languages in declaration order.
func Matrix(root string) (MatrixDoc, error) {
	config, err := project.Load(root)
	if err != nil {
		return MatrixDoc{}, &ui.Error{What: "Cannot list services", Why: err.Error(), Try: "check spinup.toml and run spinup ci services again", Err: err}
	}
	doc := MatrixDoc{Include: make([]MatrixEntry, 0, len(config.Services))}
	for _, service := range config.Services {
		if err := services.ValidName(service.Name); err != nil {
			return MatrixDoc{}, &ui.Error{What: "Cannot list services", Why: err.Error(), Try: "correct the service name in spinup.toml", Err: err}
		}
		doc.Include = append(doc.Include, MatrixEntry{
			Name: service.Name,
			Lang: DetectLang(filepath.Join(root, filepath.FromSlash(services.ContextDir(service.Name)))),
		})
	}
	return doc, nil
}

// TestDockerfile renders a test image from the supplied stack pins without reading files.
// Node checks for the optional typecheck script inside the image.
func TestDockerfile(lang string, stack *stackversions.Stack) (string, error) {
	if stack == nil {
		return "", &ui.Error{What: "Cannot render service tests", Why: "the stack pins are missing", Try: "supply the stack pins"}
	}
	var images []string
	switch lang {
	case "rust":
		images = []string{"rust"}
	case "python":
		images = []string{"python", "uv"}
	case "node":
		images = []string{"node"}
	default:
		return "", &ui.Error{What: "Cannot render service tests", Why: fmt.Sprintf("language %q has no test Dockerfile", lang), Try: "use rust, python, or node"}
	}
	refs := make([]string, 0, len(images))
	for _, name := range images {
		image, err := stack.Image(name)
		if err != nil {
			return "", &ui.Error{What: "Cannot render service tests", Why: err.Error(), Try: "supply the pinned " + name + " image", Err: err}
		}
		refs = append(refs, image.Ref())
	}
	switch lang {
	case "rust":
		return fmt.Sprintf("FROM %s\nWORKDIR /app\nCOPY . .\nRUN cargo test --locked\n", refs[0]), nil
	case "python":
		return fmt.Sprintf("FROM %s\nCOPY --from=%s /uv /bin/uv\nWORKDIR /app\nCOPY . .\nRUN uv sync --locked\nRUN uv run pytest\n", refs[0], refs[1]), nil
	default: // node
		pnpm := stack.NPM["pnpm"]
		if pnpm == "" {
			return "", &ui.Error{What: "Cannot render service tests", Why: "the pnpm pin is missing", Try: "supply the pinned pnpm version"}
		}
		return fmt.Sprintf(`FROM %s
WORKDIR /app
RUN corepack enable && corepack prepare pnpm@%s --activate
COPY . .
RUN pnpm install --frozen-lockfile
RUN pnpm test
RUN if node -e 'process.exit(Object.hasOwn(require("./package.json").scripts ?? {}, "typecheck") ? 0 : 1)'; then pnpm run typecheck; fi
`, refs[0], pnpm), nil
	}
}

// TestServiceOptions controls service test execution and output.
type TestServiceOptions struct {
	// DryRun prints only the Dockerfile and does not execute Docker.
	DryRun bool
	// Out receives the Dockerfile, test output, or a note when no tests are detected.
	// A nil writer discards output.
	Out io.Writer
	// ErrOut receives Docker diagnostics. A nil writer discards diagnostics.
	ErrOut io.Writer
	// Stack overrides the embedded stack pins when it is not nil.
	Stack *stackversions.Stack
}

// TestService runs a declared service's tests with a Dockerfile supplied on stdin.
// It never writes to the project. A nil runner uses exec.OSRunner.
func TestService(ctx context.Context, runner spexec.Runner, root, name string, opts TestServiceOptions) error {
	config, err := project.Load(root)
	if err != nil {
		return serviceTestError(name, err.Error(), "check spinup.toml and run spinup ci test-service again", err)
	}
	found := false
	for _, service := range config.Services {
		if service.Name == name {
			found = true
			break
		}
	}
	if !found {
		return serviceTestError(name, "the service is not declared in spinup.toml", "run spinup ci services to list the declared services", nil)
	}
	if err := services.ValidName(name); err != nil {
		return serviceTestError(name, err.Error(), "correct the service name in spinup.toml", err)
	}
	contextDir := services.ContextDir(name)
	dir := filepath.Join(root, filepath.FromSlash(contextDir))
	info, err := os.Stat(dir)
	if err != nil {
		return serviceTestError(name, "cannot read the service directory: "+err.Error(), "restore "+contextDir+" and run spinup ci test-service again", err)
	}
	if !info.IsDir() {
		return serviceTestError(name, contextDir+" is not a directory", "restore the service directory "+contextDir, nil)
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	lang := DetectLang(dir)
	if lang == "none" {
		if _, err := fmt.Fprintf(opts.Out, "Service %s: no tests detected\n", name); err != nil {
			return serviceTestError(name, err.Error(), "check the output destination", err)
		}
		return nil
	}
	if opts.Stack == nil {
		opts.Stack, err = stackversions.Load()
		if err != nil {
			return serviceTestError(name, err.Error(), "check the embedded stack pins", err)
		}
	}
	dockerfile, err := TestDockerfile(lang, opts.Stack)
	if err != nil {
		return err
	}
	if opts.DryRun {
		if _, err := io.WriteString(opts.Out, dockerfile); err != nil {
			return serviceTestError(name, err.Error(), "check the output destination", err)
		}
		return nil
	}
	if runner == nil {
		runner = spexec.OSRunner{}
	}
	result, err := runner.Run(ctx, spexec.Cmd{
		Name: "docker", Args: []string{"build", "-f", "-", contextDir}, Dir: root,
		Stdin: strings.NewReader(dockerfile), Stdout: opts.Out, Stderr: opts.ErrOut,
	})
	if err != nil {
		return serviceTestError(name, err.Error(), "check Docker and the test output, then run spinup ci test-service again", err)
	}
	if result.ExitCode != 0 {
		return serviceTestError(name, fmt.Sprintf("docker build returned exit code %d", result.ExitCode), "correct the test failures and run spinup ci test-service again", nil)
	}
	return nil
}

func serviceTestError(name, why, try string, err error) error {
	return &ui.Error{What: fmt.Sprintf("Cannot test service %q", name), Why: why, Try: try, Err: err}
}
