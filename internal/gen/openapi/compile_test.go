package openapi

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
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

func TestCompileGeneratedApp(t *testing.T) {
	t.Parallel()
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
	for _, file := range []string{"paths/todos.yaml", "schemas/todo.yaml"} {
		data, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		put(t, root, "api/openapi/"+file, string(data))
	}
	if err := manifest.New().Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
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
