package openapi

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/gen/run"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func TestGeneratorPlan(t *testing.T) {
	t.Parallel()
	g, ok := registry.Lookup("openapi")
	if !ok {
		t.Fatal("openapi generator is not registered")
	}
	for _, empty := range []bool{true, false} {
		t.Run(map[bool]string{true: "empty", false: "paths"}[empty], func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			put(t, root, "api/openapi/openapi.yaml", rootSpec)
			if !empty {
				put(t, root, "api/openapi/paths/todos.yaml", "/api/todos: {}")
			}
			out, err := g.Run(context.Background(), gen.Input{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Plan.Files) != 0 || !reflect.DeepEqual(out.PostSteps, []gen.PostStep{gen.PostOpenAPI}) || out.Next[0] != "cd api && go build ./..." || len(out.Next) > 2 {
				t.Fatalf("plan: %+v", out)
			}
			if len(out.Next) != 1 {
				t.Fatalf("expected only the build command: %+v", out.Next)
			}
			if _, err := os.Stat(filepath.Join(root, "api/openapi/bundle.gen.yaml")); !os.IsNotExist(err) {
				t.Fatalf("generator wrote bundle: %v", err)
			}
		})
	}
	if _, err := g.Run(context.Background(), gen.Input{Args: []string{"extra"}}); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("arguments: %v", err)
	}
}

func TestPostStep(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		paths, dry bool
		fail       string
	}{
		{name: "empty"}, {name: "paths", paths: true}, {name: "dry", paths: true, dry: true},
		{name: "tool failure", paths: true, fail: "tool"}, {name: "invalid bundle", fail: "bundle"}, {name: "write failure", fail: "write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			lock := manifest.New()
			if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
				t.Fatal(err)
			}
			put(t, root, "api/openapi/openapi.yaml", rootSpec)
			if tc.paths {
				put(t, root, "api/openapi/paths/todos.yaml", "/api/todos: {}")
			}
			if tc.fail == "bundle" {
				put(t, root, "api/openapi/schemas/todo.yaml", "Todo: {$ref: '#/components/schemas/Missing'}")
			}
			if tc.fail == "write" {
				if err := os.Mkdir(filepath.Join(root, "api/openapi/bundle.gen.yaml"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			fake := &exec.Fake{}
			if tc.paths && !tc.dry {
				e := fake.Expect("go", "tool", "-modfile=tools.mod", "oapi-codegen", "-config", "oapi-codegen.yaml", "openapi/bundle.gen.yaml")
				if tc.fail == "tool" {
					e.Fail(errors.New("tool unavailable"))
				}
			}
			g, _ := registry.Lookup("openapi")
			var output bytes.Buffer
			_, err := run.Run(context.Background(), g, nil, nil, run.Options{Root: root, Runner: fake, Out: &output, DryRun: tc.dry})
			fake.AssertDone(t)
			if tc.fail != "" {
				if err == nil || !strings.Contains(err.Error(), "Try:") {
					t.Fatalf("error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			calls := fake.Calls()
			if tc.paths && !tc.dry && (len(calls) != 1 || calls[0].Dir != filepath.Join(root, "api")) {
				t.Fatalf("calls: %+v", calls)
			}
			data, err := os.ReadFile(filepath.Join(root, "api/openapi/bundle.gen.yaml"))
			if tc.dry {
				if !os.IsNotExist(err) {
					t.Fatalf("preview wrote bundle: %v", err)
				}
				return
			}
			if err != nil || !bytes.HasPrefix(data, []byte(bundleHeader)) {
				t.Fatalf("bundle: %s, %v", data, err)
			}
			if !tc.paths && len(calls) != 0 {
				t.Fatalf("empty result: %s, %+v", output.String(), calls)
			}
			after, err := manifest.Load(filepath.Join(root, "spinup.lock"))
			if err != nil {
				t.Fatal(err)
			}
			if len(after.Files) != 0 {
				t.Fatalf("tool output in lock: %+v", after.Files)
			}
		})
	}
}

func TestPostStepEmptyNotice(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	put(t, root, "openapi/openapi.yaml", rootSpec)
	var notices bytes.Buffer
	fake := &exec.Fake{}
	if err := generate(context.Background(), root, fake, &notices); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notices.String(), "no OpenAPI paths; oapi-codegen skipped") || len(fake.Calls()) != 0 {
		t.Fatalf("notice=%s, calls=%+v", notices.String(), fake.Calls())
	}
}
