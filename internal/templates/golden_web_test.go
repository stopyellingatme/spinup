package templates_test

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

// webOutput selects the files that the web pack owns in a rendered tree.
func webOutput(path string) bool {
	return strings.HasPrefix(path, "web/") || path == "deploy/Dockerfile.web"
}

func TestGoldenWeb(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "web")
	answers := map[string]any{"app_name": "todo", "module": "example.com/todo"}
	render := func() (fs.FS, error) {
		all := testutil.RenderPack(t, []string{"base", "api", "web"}, answers)
		selected := fstest.MapFS{}
		err := fs.WalkDir(all, ".", func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !webOutput(path) {
				return err
			}
			data, err := fs.ReadFile(all, path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			selected[path] = &fstest.MapFile{Data: data, Mode: info.Mode()}
			return nil
		})
		return selected, err
	}
	fsys, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "web", fsys)
	testutil.Deterministic(t, render)
}

// TestWebPins checks that the web pack takes every version from the stack versions.
func TestWebPins(t *testing.T) {
	t.Parallel()
	stack := stackversions.MustLoad()
	fsys := testutil.RenderPack(t, []string{"base", "api", "web"}, map[string]any{"app_name": "todo", "module": "example.com/todo"})

	data, err := fs.ReadFile(fsys, "web/package.json")
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		PackageManager  string            `json:"packageManager"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatalf("web/package.json: %v", err)
	}
	for _, deps := range []map[string]string{pkg.Dependencies, pkg.DevDependencies} {
		for name, version := range deps {
			want, err := stack.NPMVersion(name)
			if err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			if version != want {
				t.Errorf("%s = %q, want the stack pin %q", name, version, want)
			}
		}
	}

	docker, err := fs.ReadFile(fsys, "deploy/Dockerfile.web")
	if err != nil {
		t.Fatal(err)
	}
	node, err := stack.Image("node")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(docker), "FROM "+node.Ref()); got != 2 {
		t.Errorf("Dockerfile.web has %d stages on %s, want 2", got, node.Ref())
	}
	for _, want := range []string{"ARG PUBLIC_ORIGIN", "pnpm exec svelte-kit sync", "USER node", "HEALTHCHECK", "BODY_SIZE_LIMIT=", "SHUTDOWN_TIMEOUT="} {
		if !strings.Contains(string(docker), want) {
			t.Errorf("Dockerfile.web does not contain %q", want)
		}
	}
}

// TestWebPackClasses checks the file classes that upgrades depend on (ADR 0008 §2).
func TestWebPackClasses(t *testing.T) {
	t.Parallel()
	pack, err := templates.Load(templates.Embedded(), "web")
	if err != nil {
		t.Fatal(err)
	}
	classes := map[string]string{}
	for _, file := range pack.Files {
		classes[file.Path] = file.Class
	}
	for path, want := range map[string]string{
		"web/vite.config.js":                         "managed",
		"web/tsconfig.json":                          "managed",
		"web/components.json":                        "scaffold",
		"web/src/app.html":                           "scaffold",
		"web/src/app.css":                            "scaffold",
		"web/src/app.d.ts":                           "scaffold",
		"web/src/hooks.server.ts":                    "scaffold",
		"web/src/lib/utils.ts":                       "scaffold",
		"web/src/lib/api/client.ts":                  "scaffold",
		"web/src/lib/api/schema.d.ts":                "seed",
		"web/src/routes/+layout.svelte":              "scaffold",
		"web/src/routes/+layout.server.ts":           "scaffold",
		"web/src/routes/+page.svelte":                "scaffold",
		"web/src/routes/healthz/+server.ts":          "managed",
		"deploy/Dockerfile.web":                      "managed",
		"web/pnpm-lock.yaml":                         "seed",
		"web/src/lib/components/ui/card/card.svelte": "seed",
	} {
		if got := classes[path]; got != want {
			t.Errorf("%s: class %q, want %q", path, got, want)
		}
	}
	var registry *templates.Question
	for i := range pack.Questions {
		if pack.Questions[i].Key == "shadcn_registry" {
			registry = &pack.Questions[i]
		}
	}
	if registry == nil || registry.Type != "string" || registry.Default != "1.7.0" {
		t.Errorf("shadcn_registry question = %+v, want a string with default 1.7.0", registry)
	}
}
