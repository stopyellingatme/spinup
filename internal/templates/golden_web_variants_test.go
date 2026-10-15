package templates_test

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestGoldenWebVariants(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "web-variants")
	for _, mode := range []string{"node", "embed"} {
		for _, pm := range []string{"pnpm", "bun"} {
			for _, api := range []string{"openapi-fetch", "heyapi"} {
				t.Run(mode+"/"+pm+"/"+api, func(t *testing.T) {
					t.Parallel()
					render := func() (fs.FS, error) {
						all := testutil.RenderPack(t, []string{"base", "api", "web", "ci", "deploy"}, map[string]any{"app_name": "todo", "module": "example.com/todo", "frontend_mode": mode, "package_manager": pm, "api_client": api})
						selected := fstest.MapFS{}
						err := fs.WalkDir(all, ".", func(path string, entry fs.DirEntry, err error) error {
							if err != nil || entry.IsDir() {
								return err
							}
							if !strings.HasPrefix(path, "web/src/lib/api/") && path != "web/package.json" && path != "web/pnpm-lock.yaml" && path != "web/pnpm-workspace.yaml" && path != "web/bun.lock" && path != "deploy/Dockerfile.web" && path != ".github/workflows/ci.yml" && path != ".github/workflows/e2e.yml" {
								return nil
							}
							data, err := fs.ReadFile(all, path)
							if err != nil {
								return err
							}
							selected[path] = &fstest.MapFile{Data: data, Mode: 0644}
							return nil
						})
						return selected, err
					}
					tree, err := render()
					if err != nil {
						t.Fatal(err)
					}
					lockfile, absent := "web/pnpm-lock.yaml", "web/bun.lock"
					if pm == "bun" {
						lockfile, absent = absent, lockfile
					}
					if _, err := fs.Stat(tree, lockfile); err != nil {
						t.Fatal(err)
					}
					if _, err := fs.Stat(tree, absent); !errors.Is(err, fs.ErrNotExist) {
						t.Fatalf("unexpected lockfile %s", absent)
					}
					for _, path := range []string{".github/workflows/ci.yml", ".github/workflows/e2e.yml"} {
						data, err := fs.ReadFile(tree, path)
						if err != nil {
							t.Fatal(err)
						}
						if pm == "bun" && (!strings.Contains(string(data), "npm install --global bun@"+stackversions.MustLoad().NPM["bun"]) || strings.Contains(string(data), "pnpm")) {
							t.Fatalf("bun workflow %s:\n%s", path, data)
						}
					}
					client, err := fs.ReadFile(tree, "web/src/lib/api/client.ts")
					if err != nil {
						t.Fatal(err)
					}
					if api == "heyapi" && !strings.Contains(string(client), "./client/client.gen") {
						t.Fatal("heyapi wrapper does not import the generated client")
					}
					if mode == "node" && pm == "bun" {
						data, err := fs.ReadFile(tree, "deploy/Dockerfile.web")
						if err != nil || strings.Contains(string(data), "pnpm") || !strings.Contains(string(data), "bun.lock") {
							t.Fatalf("bun Dockerfile: %s, %v", data, err)
						}
					}
					testutil.Golden(t, "web-variants/"+mode+"-"+pm+"-"+api, tree)
					testutil.Deterministic(t, render)
				})
			}
		}
	}
}

func TestBunWorkflows(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"node", "embed"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			tree := renderCI(t, true, map[string]any{"app_name": "todo", "modules": "ci,deploy,backups", "package_manager": "bun", "frontend_mode": mode, "api_client": "heyapi"})
			checkCITree(t, tree)
			workflow := readCIFile(t, tree, ".github/workflows/e2e.yml")
			if mode == "embed" && (!strings.Contains(workflow, "go run -tags embedweb") || strings.Contains(workflow, "node build")) {
				t.Fatal("embed CI does not serve the embedded build")
			}
		})
	}
}
