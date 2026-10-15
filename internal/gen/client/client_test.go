package client

import (
	"bytes"
	"context"
	"fmt"
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

func TestGenerator(t *testing.T) {
	t.Parallel()
	g, ok := registry.Lookup("client")
	if !ok {
		t.Fatal("client is not registered")
	}
	out, err := g.Run(context.Background(), gen.Input{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Plan.Files) != 0 || !reflect.DeepEqual(out.PostSteps, []gen.PostStep{gen.PostOpenAPI, gen.PostClient}) || !reflect.DeepEqual(out.Next, []string{"cd web && pnpm check"}) {
		t.Fatalf("output = %+v", out)
	}
	if _, err := g.Run(context.Background(), gen.Input{Args: []string{"unexpected"}}); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("argument error = %v", err)
	}
}

func TestGenerate(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                 string
		web, bundle, modules bool
		failure              string
		skip                 string
	}{
		{name: "generate", web: true, bundle: true, modules: true},
		{name: "install first", web: true, bundle: true},
		{name: "no web", skip: "no web pack"},
		{name: "no bundle", web: true, skip: "bundle.gen.yaml is missing"},
		{name: "missing pnpm", web: true, bundle: true, modules: true, failure: "missing"},
		{name: "generation failure", web: true, bundle: true, modules: true, failure: "generate"},
		{name: "install failure", web: true, bundle: true, failure: "install"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			lock := manifest.New()
			if tt.web {
				lock.Packs = []string{"base", "api", "web"}
			}
			if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
				t.Fatal(err)
			}
			if tt.bundle {
				write(t, root, "api/openapi/bundle.gen.yaml", "openapi: 3.0.0\n")
			}
			if tt.modules {
				if err := os.MkdirAll(filepath.Join(root, "web/node_modules"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			var fake exec.Fake
			var want [][]string
			if tt.skip == "" {
				if !tt.modules {
					want = append(want, []string{"--dir", "web", "install", "--frozen-lockfile"})
				}
				if tt.failure != "install" {
					want = append(want, []string{"--dir", "web", "exec", "openapi-typescript", "../api/openapi/bundle.gen.yaml", "-o", "src/lib/api/schema.d.ts"})
				}
				for _, args := range want {
					e := fake.Expect("pnpm", args...)
					if tt.failure == "missing" {
						e.Fail(&exec.NotFoundError{Name: "pnpm"})
					} else if tt.failure != "" {
						e.Return("", "tool failed", 1)
					}
				}
			}
			var notices bytes.Buffer
			err := generate(context.Background(), root, &fake, &notices)
			if tt.failure != "" {
				if err == nil || !strings.Contains(err.Error(), "Try:") {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if tt.skip != "" && !strings.Contains(notices.String(), tt.skip) {
				t.Fatalf("notice = %q", notices.String())
			}
			calls := fake.Calls()
			if len(calls) != len(want) {
				t.Fatalf("calls = %+v", calls)
			}
			for i, c := range calls {
				if c.Dir != root || c.Name != "pnpm" || !reflect.DeepEqual(c.Args, want[i]) {
					t.Fatalf("command = %+v", c)
				}
			}
			fake.AssertDone(t)
		})
	}
}

func write(t *testing.T, root, path, data string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestRegisteredPostStepAndPreview(t *testing.T) {
	t.Parallel()
	for _, preview := range []bool{true, false} {
		t.Run(fmt.Sprintf("preview=%t", preview), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			lock := manifest.New()
			lock.Packs = []string{"base", "api", "web"}
			if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
				t.Fatal(err)
			}
			write(t, root, "api/openapi/bundle.gen.yaml", "bundle")
			if err := os.MkdirAll(filepath.Join(root, "web/node_modules"), 0755); err != nil {
				t.Fatal(err)
			}
			var fake exec.Fake
			if !preview {
				fake.Expect("pnpm", "--dir", "web", "exec", "openapi-typescript", "../api/openapi/bundle.gen.yaml", "-o", "src/lib/api/schema.d.ts")
			}
			g := gen.Generator{Name: "client-post-test", Run: func(context.Context, gen.Input) (*gen.Output, error) {
				return &gen.Output{PostSteps: []gen.PostStep{gen.PostClient}}, nil
			}}
			before, err := os.ReadFile(filepath.Join(root, "spinup.lock"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := run.Run(context.Background(), g, nil, nil, run.Options{Root: root, DryRun: preview, Runner: &fake}); err != nil {
				t.Fatal(err)
			}
			fake.AssertDone(t)
			after, err := os.ReadFile(filepath.Join(root, "spinup.lock"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("lock changed: %v", err)
			}
		})
	}
}

func TestVariants(t *testing.T) {
	t.Parallel()
	for _, pm := range []string{"pnpm", "bun"} {
		for _, api := range []string{"openapi-fetch", "heyapi"} {
			t.Run(pm+"/"+api, func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				lock := manifest.New()
				lock.Packs = []string{"base", "api", "web"}
				lock.Answers = map[string]any{"package_manager": pm, "api_client": api}
				if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
					t.Fatal(err)
				}
				write(t, root, "api/openapi/bundle.gen.yaml", "openapi: 3.0.3\n")
				var fake exec.Fake
				name, prefix := pm, []string{"--dir", "web"}
				install := append(append([]string{}, prefix...), "install", "--frozen-lockfile")
				if pm == "bun" {
					install = []string{"install", "--frozen-lockfile"}
				}
				fake.Expect(pm, install...)
				execPrefix := append(append([]string{}, prefix...), "exec")
				if pm == "bun" {
					name = "bunx"
					execPrefix = []string{"--no-install"}
				}
				if api == "heyapi" {
					fake.Expect(name, append(append([]string{}, execPrefix...), "openapi-ts", "--input", "../api/openapi/bundle.gen.yaml", "--output", "src/lib/api/client")...)
				}
				fake.Expect(name, append(append([]string{}, execPrefix...), "openapi-typescript", "../api/openapi/bundle.gen.yaml", "-o", "src/lib/api/schema.d.ts")...)
				var notices bytes.Buffer
				if err := generate(t.Context(), root, &fake, &notices); err != nil {
					t.Fatal(err)
				}
				fake.AssertDone(t)
				for _, call := range fake.Calls() {
					wantDir := root
					if pm == "bun" {
						wantDir = filepath.Join(root, "web")
					}
					if call.Dir != wantDir {
						t.Fatalf("command directory = %s, want %s", call.Dir, wantDir)
					}
				}
				out, err := plan(t.Context(), gen.Input{Lock: lock})
				if err != nil {
					t.Fatal(err)
				}
				want := "cd web && pnpm check"
				if pm == "bun" {
					want = "cd web && bun run check"
				}
				if out.Next[0] != want {
					t.Fatalf("next = %v", out.Next)
				}
			})
		}
	}
}
