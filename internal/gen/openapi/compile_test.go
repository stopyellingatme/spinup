package openapi

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/gen/run"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

type offlineRunner struct{}

// Run executes commands without network access or the repository's workspace.
func (offlineRunner) Run(ctx context.Context, cmd exec.Cmd) (exec.Result, error) {
	cmd.Env = append(cmd.Env, "GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "GOPROXY=off", "GOSUMDB=off")
	return (exec.OSRunner{}).Run(ctx, cmd)
}

// newApp renders the base and api packs into a temporary app with the todo
// fragments, and points its kit at this checkout.
func newApp(t *testing.T, fragments ...string) string {
	t.Helper()
	root := t.TempDir()
	tree := testutil.RenderPack(t, []string{"base", "api"}, map[string]any{"app_name": "todo", "module": "example.com/todo"})
	if err := fs.WalkDir(tree, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(tree, name)
		if err != nil {
			return err
		}
		put(t, root, name, string(data))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	kit := filepath.Join(testutil.RepoRoot(t), "kit")
	modFile := filepath.Join(root, "api/go.mod")
	mod, err := os.ReadFile(modFile)
	if err != nil {
		t.Fatal(err)
	}
	mod = append(mod, []byte("\nreplace github.com/stopyellingatme/spinup/kit => "+strconv.Quote(kit)+"\n")...)
	if err := os.WriteFile(modFile, mod, 0644); err != nil {
		t.Fatal(err)
	}
	for _, file := range fragments {
		data, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		put(t, root, "api/openapi/"+file, string(data))
	}
	if err := manifest.New().Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCompileGeneratedApp(t *testing.T) {
	t.Parallel()
	root := newApp(t, "paths/todos.yaml", "schemas/todo.yaml")
	g, ok := registry.Lookup("openapi")
	if !ok {
		t.Fatal("openapi generator missing")
	}
	runner := offlineRunner{}
	if _, err := run.Run(context.Background(), g, nil, nil, run.Options{Root: root, Runner: runner}); err != nil {
		t.Fatal(err)
	}
	generated, err := os.ReadFile(filepath.Join(root, "api/internal/gen/api.gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(generated, []byte("v2.8.0")) || !bytes.Contains(generated, []byte("StrictServerInterface")) {
		t.Fatalf("unexpected generated server:\n%s", generated)
	}
	result, err := runner.Run(context.Background(), exec.Cmd{Name: "go", Args: []string{"build", "./..."}, Dir: filepath.Join(root, "api")})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("generated app build: %v\n%s%s", err, result.Stdout, result.Stderr)
	}
}

// TestDocumentedWiringCompiles builds an app with a hand-written path and the
// wiring that docs/commands/generate-openapi.md shows. The app has no resource,
// so registerGenerated stays empty and the guide must give the wiring.
func TestDocumentedWiringCompiles(t *testing.T) {
	t.Parallel()
	root := newApp(t)
	put(t, root, "api/openapi/paths/ping.yaml", "/api/ping:\n  get:\n    operationId: ping\n    summary: Check that the API answers\n    responses:\n      \"200\":\n        description: The API is up.\n")
	g, ok := registry.Lookup("openapi")
	if !ok {
		t.Fatal("openapi generator missing")
	}
	runner := offlineRunner{}
	if _, err := run.Run(context.Background(), g, nil, nil, run.Options{Root: root, Runner: runner}); err != nil {
		t.Fatal(err)
	}
	guide, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "docs/commands/generate-openapi.md"))
	if err != nil {
		t.Fatal(err)
	}
	files := wiringFiles(string(guide))
	if _, ok := files["api/internal/routes/ping.go"]; !ok || len(files) < 2 {
		t.Fatalf("the guide must show the handler and the wiring with wiring-file comments, found %v", files)
	}
	for name, code := range files {
		put(t, root, name, code)
	}
	result, err := runner.Run(context.Background(), exec.Cmd{Name: "go", Args: []string{"vet", "./..."}, Dir: filepath.Join(root, "api")})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("documented wiring build: %v\n%s%s", err, result.Stdout, result.Stderr)
	}
}

// wiringFiles returns the code blocks that follow a wiring-file comment, by path.
func wiringFiles(guide string) map[string]string {
	files := map[string]string{}
	lines := strings.Split(guide, "\n")
	for i := 0; i+1 < len(lines); i++ {
		name, ok := strings.CutPrefix(lines[i], "<!-- wiring-file: ")
		name, closed := strings.CutSuffix(name, " -->")
		if !ok || !closed || !strings.HasPrefix(lines[i+1], "```") {
			continue
		}
		var code []string
		for j := i + 2; j < len(lines) && !strings.HasPrefix(lines[j], "```"); j++ {
			code = append(code, lines[j])
		}
		files[name] = strings.Join(code, "\n") + "\n"
	}
	return files
}
