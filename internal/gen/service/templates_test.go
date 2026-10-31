package service_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/stopyellingatme/spinup/internal/services"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func templateFile(t *testing.T, lang, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "internal/gen/service/templates", lang, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestTemplatePins(t *testing.T) {
	t.Parallel()
	stack, err := stackversions.Load()
	if err != nil {
		t.Fatal(err)
	}
	var cargo struct {
		Dependencies    map[string]any `toml:"dependencies"`
		DevDependencies map[string]any `toml:"dev-dependencies"`
	}
	if err := toml.Unmarshal(templateFile(t, "rust", "Cargo.toml"), &cargo); err != nil {
		t.Fatal(err)
	}
	if len(cargo.Dependencies)+len(cargo.DevDependencies) != len(stack.Crates) {
		t.Fatal("Cargo dependency set differs from stack pins")
	}
	for name, version := range stack.Crates {
		dep := cargo.Dependencies[name]
		if dep == nil {
			dep = cargo.DevDependencies[name]
		}
		if fields, ok := dep.(map[string]any); ok {
			dep = fields["version"]
		}
		if dep != "="+version {
			t.Errorf("crate %s = %v, want =%s", name, dep, version)
		}
	}
	for _, name := range []string{"opentelemetry", "opentelemetry_sdk", "opentelemetry-otlp", "opentelemetry-http", "tracing-opentelemetry"} {
		if stack.Crates[name] == "" {
			t.Errorf("crate %s is missing", name)
		}
	}
	for _, name := range []string{"opentelemetry", "opentelemetry_sdk", "opentelemetry-otlp", "opentelemetry-http"} {
		if stack.Crates[name] != stack.Crates["opentelemetry"] {
			t.Errorf("crate %s = %s, want %s", name, stack.Crates[name], stack.Crates["opentelemetry"])
		}
	}
	var python struct {
		Project struct {
			Dependencies []string `toml:"dependencies"`
		} `toml:"project"`
		Groups struct {
			Dev []string `toml:"dev"`
		} `toml:"dependency-groups"`
	}
	if err := toml.Unmarshal(templateFile(t, "python", "pyproject.toml"), &python); err != nil {
		t.Fatal(err)
	}
	deps := slices.Concat(python.Project.Dependencies, python.Groups.Dev)
	if len(deps) != len(stack.PythonPackages) {
		t.Fatal("Python dependency set differs from stack pins")
	}
	for name, version := range stack.PythonPackages {
		if !slices.Contains(deps, name+"=="+version) {
			t.Errorf("Python dependency %s==%s is missing", name, version)
		}
	}
	for _, name := range []string{"opentelemetry-api", "opentelemetry-sdk", "opentelemetry-exporter-otlp-proto-http", "opentelemetry-instrumentation-fastapi"} {
		if stack.PythonPackages[name] == "" {
			t.Errorf("Python package %s is missing", name)
		}
	}
	for _, name := range []string{"opentelemetry-api", "opentelemetry-sdk", "opentelemetry-exporter-otlp-proto-http"} {
		if stack.PythonPackages[name] != stack.PythonPackages["opentelemetry-api"] {
			t.Errorf("Python package %s = %s, want %s", name, stack.PythonPackages[name], stack.PythonPackages["opentelemetry-api"])
		}
	}
	var node struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
		PackageManager  string            `json:"packageManager"`
	}
	if err := json.Unmarshal(templateFile(t, "node", "package.json"), &node); err != nil {
		t.Fatal(err)
	}
	if got, want := len(node.Dependencies)+len(node.DevDependencies), len(stack.NodePackages)+1; got != want {
		t.Fatalf("Node dependency count = %d, want %d", got, want)
	}
	for name, version := range stack.NodePackages {
		dependency, hasDependency := node.Dependencies[name]
		devDependency, hasDevDependency := node.DevDependencies[name]
		if hasDependency == hasDevDependency {
			t.Errorf("Node package %s must occur in exactly one dependency set", name)
			continue
		}
		if got := dependency + devDependency; got != version {
			t.Errorf("Node package %s = %q, want %q", name, got, version)
		}
	}
	if node.DevDependencies["typescript"] != stack.NPM["typescript"] {
		t.Errorf("typescript = %q, want %q", node.DevDependencies["typescript"], stack.NPM["typescript"])
	}
	if node.PackageManager != "pnpm@"+stack.NPM["pnpm"] {
		t.Errorf("packageManager = %q, want pnpm@%s", node.PackageManager, stack.NPM["pnpm"])
	}
}

func TestTemplateLockfilesAndAPI(t *testing.T) {
	t.Parallel()
	var apis [][]byte
	for _, tc := range []struct {
		lang      string
		lockfiles []string
	}{
		{"rust", []string{"Cargo.toml", "Cargo.lock"}},
		{"python", []string{"pyproject.toml", "uv.lock"}},
		{"node", nodeLockfiles(t)},
	} {
		in := input(t, config, "differentname", "--lang", tc.lang)
		out, err := generator(t).Run(t.Context(), in)
		if err != nil {
			t.Fatal(err)
		}
		files := out.Plan.FS()
		for _, name := range tc.lockfiles {
			got, err := fs.ReadFile(files, services.ContextDir("differentname")+"/"+name)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, templateFile(t, tc.lang, name)) {
				t.Errorf("%s changed", name)
			}
		}
		api, err := fs.ReadFile(files, services.ContextDir("differentname")+"/openapi.yaml")
		if err != nil {
			t.Fatal(err)
		}
		apis = append(apis, api)
	}
	for i := 1; i < len(apis); i++ {
		if !bytes.Equal(apis[0], apis[i]) {
			t.Fatal("the service APIs differ")
		}
	}
}

func nodeLockfiles(t *testing.T) []string {
	t.Helper()
	lockfiles := []string{"package.json", "pnpm-lock.yaml"}
	if _, err := os.Stat(filepath.Join(testutil.RepoRoot(t), "internal/gen/service/templates/node/pnpm-workspace.yaml")); err == nil {
		lockfiles = append(lockfiles, "pnpm-workspace.yaml")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return lockfiles
}

func TestRenderedOptions(t *testing.T) {
	t.Parallel()
	stack, err := stackversions.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"rust", "python", "node"} {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			in := input(t, config, "scanner", "--lang", lang, "--port", "9001")
			out, err := generator(t).Run(t.Context(), in)
			if err != nil {
				t.Fatal(err)
			}
			files := out.Plan.FS()
			docker, err := fs.ReadFile(files, services.Dockerfile("scanner"))
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"ENV PORT=9001", "EXPOSE 9001"}
			switch lang {
			case "rust":
				want = append(want, "FROM "+stack.Images["rust"].Ref()+" AS build", "FROM "+stack.Images["distroless-static-debian13"].Ref(), "cargo build --release --locked", "USER nonroot", `ENTRYPOINT ["/service"]`)
			case "node":
				want = append(want, "FROM "+stack.Images["node"].Ref(), "pnpm install --frozen-lockfile --prod --ignore-scripts", "USER node", `CMD ["node", "src/main.ts"]`)
			case "python":
				want = append(want, "FROM "+stack.Images["python"].Ref(), "COPY --from="+stack.Images["uv"].Ref()+" /uv /bin/uv", "uv sync --locked --no-dev", "USER service", `"--port", "9001"`)
			default:
				t.Fatalf("unexpected language %q", lang)
			}
			for _, fragment := range want {
				if !strings.Contains(string(docker), fragment) {
					t.Errorf("Dockerfile does not contain %q", fragment)
				}
			}
			readme, err := fs.ReadFile(files, services.ContextDir("scanner")+"/README.md")
			if err != nil {
				t.Fatal(err)
			}
			for _, fragment := range []string{"# scanner", "belongs to todo", "9001"} {
				if !strings.Contains(string(readme), fragment) {
					t.Errorf("README does not contain %q", fragment)
				}
			}
		})
	}
}

func TestTemplateRustTelemetryRenders(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"ocr", "scanner"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out, err := generator(t).Run(t.Context(), input(t, config, name, "--lang", "rust"))
			if err != nil {
				t.Fatal(err)
			}
			files := out.Plan.FS()
			data, err := fs.ReadFile(files, services.ContextDir(name)+"/src/telemetry.rs")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), `"todo-`+name+`"`) {
				t.Fatal("telemetry has no rendered service name")
			}
			if strings.Contains(string(data), "{{") {
				t.Fatal("telemetry contains an unexpanded template action")
			}
			main, err := fs.ReadFile(files, services.ContextDir(name)+"/src/main.rs")
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"alloy", "4318", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_SERVICE_NAME", "OTEL_RESOURCE_ATTRIBUTES"} {
				if strings.Contains(string(main), forbidden) {
					t.Fatalf("main.rs contains %q", forbidden)
				}
			}
		})
	}
}

func TestTemplatePythonTelemetryRenders(t *testing.T) {
	t.Parallel()
	out, err := generator(t).Run(t.Context(), input(t, config, "ocr", "--lang", "python"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(out.Plan.FS(), services.ContextDir("ocr")+"/service/telemetry.py"); err != nil {
		t.Fatal(err)
	}
}
