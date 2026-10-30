package service_test

import (
	"bytes"
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
	if !bytes.Equal(apis[0], apis[1]) {
		t.Fatal("the service APIs differ")
	}
}

func TestRenderedOptions(t *testing.T) {
	t.Parallel()
	stack, err := stackversions.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"rust", "python"} {
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
			if lang == "rust" {
				want = append(want, "FROM "+stack.Images["rust"].Ref()+" AS build", "FROM "+stack.Images["distroless-static-debian13"].Ref(), "cargo build --release --locked", "USER nonroot", `ENTRYPOINT ["/service"]`)
			} else {
				want = append(want, "FROM "+stack.Images["python"].Ref(), "COPY --from="+stack.Images["uv"].Ref()+" /uv /bin/uv", "uv sync --locked --no-dev", "USER service", `"--port", "9001"`)
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
