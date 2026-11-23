package templates_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/render"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/testutil"
	"go.yaml.in/yaml/v3"
)

// consolePriorFS substitutes the frozen Dockerfile from before the console pack.
type consolePriorFS struct{ fs.FS }

func (f consolePriorFS) Open(name string) (fs.File, error) {
	if name == "files/deploy/Dockerfile.api.tmpl" {
		return os.Open("../../testdata/golden/console/prior/Dockerfile.api.tmpl")
	}
	return f.FS.Open(name)
}

func renderConsole(t *testing.T, mode, profile string, enabled, prior bool) fs.FS {
	t.Helper()
	names := []string{"base", "api", "deploy"}
	modules := []string{"deploy"}
	answers := map[string]any{"app_name": "todo", "module": "example.com/todo", "agent_guidance": true}
	if mode != "api" {
		names = append(names, "web")
		answers["frontend_mode"] = mode
	}
	if enabled {
		names = append(names, "console")
		modules = append(modules, "console")
	}
	if profile != "absent" {
		names = append(names, "monitoring")
		modules = append(modules, "monitoring")
		answers["monitoring_profile"] = profile
	}
	answers["modules"] = strings.Join(modules, ",")
	var packs []*templates.Pack
	for _, name := range names {
		pack, err := templates.Load(templates.Embedded(), name)
		if err != nil {
			t.Fatal(err)
		}
		if prior && name == "api" {
			pack.FS = consolePriorFS{pack.FS}
		}
		packs = append(packs, pack)
	}
	plan, err := render.Render(render.Input{Packs: packs, Answers: answers, Stack: stackversions.MustLoad(), TemplateVersion: "0.0.0-test"})
	if err != nil {
		t.Fatal(err)
	}
	return plan.FS()
}

type consoleCompose struct {
	Name     string                    `yaml:"name"`
	Services map[string]consoleService `yaml:"services"`
	Networks map[string]struct {
		Name     string `yaml:"name"`
		Driver   string `yaml:"driver"`
		External bool   `yaml:"external"`
	} `yaml:"networks"`
}

// KnownFields rejects extra mounts, namespaces, secrets, and service dependencies.
type consoleService struct {
	Image       string            `yaml:"image"`
	Entrypoint  []string          `yaml:"entrypoint"`
	User        string            `yaml:"user"`
	ReadOnly    bool              `yaml:"read_only"`
	CapDrop     []string          `yaml:"cap_drop"`
	SecurityOpt []string          `yaml:"security_opt"`
	PIDs        int               `yaml:"pids_limit"`
	CPUs        string            `yaml:"cpus"`
	Memory      string            `yaml:"mem_limit"`
	Restart     string            `yaml:"restart"`
	Environment map[string]string `yaml:"environment"`
	Ports       []string          `yaml:"ports"`
	Volumes     []string          `yaml:"volumes"`
	Networks    []string          `yaml:"networks"`
	Profiles    []string          `yaml:"profiles"`
	Healthcheck struct {
		Test     []string `yaml:"test"`
		Interval string   `yaml:"interval"`
		Timeout  string   `yaml:"timeout"`
		Retries  int      `yaml:"retries"`
	} `yaml:"healthcheck"`
	Logging struct {
		Driver  string            `yaml:"driver"`
		Options map[string]string `yaml:"options"`
	} `yaml:"logging"`
}

func parseConsoleCompose(t *testing.T, tree fs.FS, name string) consoleCompose {
	t.Helper()
	data := readDeployFile(t, tree, name)
	var compose consoleCompose
	decoder := yaml.NewDecoder(strings.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&compose); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return compose
}

func checkConsoleService(t *testing.T, service consoleService, query bool) {
	t.Helper()
	command, memory, heap := "serve", "64m", "40MiB"
	health := []string{"CMD", "/console", "healthcheck"}
	env := map[string]string{
		"CONSOLE_APP":     "${CONSOLE_APP:?spinup deploy sets CONSOLE_APP}",
		"CONSOLE_ENV":     "${CONSOLE_ENV:?spinup deploy sets CONSOLE_ENV}",
		"CONSOLE_PROFILE": "${CONSOLE_PROFILE:?spinup deploy sets CONSOLE_PROFILE}",
	}
	if query {
		command, memory, heap = "query", "48m", "28MiB"
		health = append(health, "query")
		if len(service.Ports) != 0 || len(service.Volumes) != 0 || !slices.Equal(service.Profiles, []string{"query"}) || !slices.Equal(service.Networks, []string{"console", "monitoring"}) {
			t.Errorf("relay isolation is incorrect: %+v", service)
		}
	} else {
		env["CONSOLE_ORIGIN"] = "${CONSOLE_ORIGIN:-}"
		env["CONSOLE_BACKEND"] = "${CONSOLE_BACKEND:?spinup deploy sets CONSOLE_BACKEND}"
		env["CONSOLE_JOBS"] = "${CONSOLE_JOBS:?spinup deploy sets CONSOLE_JOBS}"
		if !slices.Equal(service.Ports, []string{"127.0.0.1:7410:7410/tcp"}) || !slices.Equal(service.Volumes, []string{"/opt/todo/console/data:/data:ro"}) || !slices.Equal(service.Networks, []string{"console"}) || len(service.Profiles) != 0 {
			t.Errorf("page isolation is incorrect: %+v", service)
		}
	}
	env["GOMEMLIMIT"] = heap
	if !reflect.DeepEqual(service.Environment, env) {
		t.Errorf("environment = %v, want %v", service.Environment, env)
	}
	if service.Image != "${CONSOLE_IMAGE:?spinup deploy sets CONSOLE_IMAGE}" || !slices.Equal(service.Entrypoint, []string{"/console", command}) {
		t.Errorf("console must use the recorded API image and its own entrypoint: %+v", service)
	}
	if service.User != "65532:65532" || !service.ReadOnly || !slices.Equal(service.CapDrop, []string{"ALL"}) || !slices.Equal(service.SecurityOpt, []string{"no-new-privileges:true"}) || service.PIDs != 32 || service.CPUs != "0.25" || service.Memory != memory || service.Restart != "unless-stopped" {
		t.Errorf("hardening or limits are incorrect: %+v", service)
	}
	if !slices.Equal(service.Healthcheck.Test, health) || service.Healthcheck.Interval != "15s" || service.Healthcheck.Timeout != "3s" || service.Healthcheck.Retries != 3 {
		t.Errorf("health check = %+v", service.Healthcheck)
	}
	if service.Logging.Driver != "json-file" || !reflect.DeepEqual(service.Logging.Options, map[string]string{"max-size": "10m", "max-file": "3"}) {
		t.Errorf("logging = %+v", service.Logging)
	}
}

func TestGoldenConsole(t *testing.T) {
	t.Parallel()
	if !testutil.GoldenSelected(t, "console") {
		return
	}
	t.Run("Isolation", func(t *testing.T) {
		tree := renderConsole(t, "api", "lite", true, false)
		base := parseConsoleCompose(t, tree, "console/compose.console.yml")
		if base.Name != "todo-console" || len(base.Services) != 1 || len(base.Networks) != 1 {
			t.Fatalf("base must contain only the page and its bridge: %+v", base)
		}
		network := base.Networks["console"]
		if network.Name != "todo-console" || network.Driver != "bridge" || network.External {
			t.Errorf("console bridge = %+v", network)
		}
		checkConsoleService(t, base.Services["console"], false)
		overlay := parseConsoleCompose(t, tree, "console/compose.query.yml")
		if overlay.Name != "" || len(overlay.Services) != 1 || len(overlay.Networks) != 1 {
			t.Fatalf("overlay must contain only the relay and its external network: %+v", overlay)
		}
		network = overlay.Networks["monitoring"]
		if network.Name != "todo-monitoring" || !network.External || network.Driver != "" {
			t.Errorf("monitoring network = %+v", network)
		}
		checkConsoleService(t, overlay.Services["console-query"], true)
	})
	t.Run("Profiles", func(t *testing.T) {
		t.Run("AbsentWithOldAnswer", func(t *testing.T) {
			pack, err := templates.Load(templates.Embedded(), "console")
			if err != nil {
				t.Fatal(err)
			}
			plan, err := render.Render(render.Input{
				Packs: []*templates.Pack{pack}, Stack: stackversions.MustLoad(),
				Answers: map[string]any{"app_name": "todo", "modules": []string{"deploy", "console"}, "monitoring_profile": "lite"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if readDeployFile(t, plan.FS(), "console/compose.query.yml") != "services: {}\n" {
				t.Error("an old monitoring answer enabled the relay without the monitoring module")
			}
		})
		for _, profile := range []string{"absent", "none", "lite", "full"} {
			for _, mode := range []string{"api", "embed", "node"} {
				t.Run(profile+"/"+mode, func(t *testing.T) {
					renderTree := func() (fs.FS, error) { return renderConsole(t, mode, profile, true, false), nil }
					tree, _ := renderTree()
					selected := fstest.MapFS{}
					for _, name := range []string{"api/cmd/console/main.go", "console/compose.console.yml", "console/compose.query.yml"} {
						data, err := fs.ReadFile(tree, name)
						if err != nil {
							t.Fatal(err)
						}
						selected[name] = &fstest.MapFile{Data: data, Mode: 0o644}
					}
					overlay := parseConsoleCompose(t, tree, "console/compose.query.yml")
					active := profile == "lite" || profile == "full"
					if active {
						checkConsoleService(t, overlay.Services["console-query"], true)
					} else if string(selected["console/compose.query.yml"].Data) != "services: {}\n" {
						t.Error("inactive monitoring must render only services: {}")
					}
					base := parseConsoleCompose(t, tree, "console/compose.console.yml")
					checkConsoleService(t, base.Services["console"], false)
					// All app modes must render the same independent console.
					if mode == "api" {
						testutil.Golden(t, "console/"+profile, selected)
					} else {
						api := renderConsole(t, "api", profile, true, false)
						for name, file := range selected {
							if string(file.Data) != readDeployFile(t, api, name) {
								t.Errorf("%s depends on frontend mode", name)
							}
						}
					}
					testutil.Deterministic(t, renderTree)
				})
			}
		}
	})
	t.Run("Image", func(t *testing.T) {
		for _, mode := range []string{"api", "embed", "node"} {
			t.Run(mode, func(t *testing.T) {
				tree := renderConsole(t, mode, "absent", true, false)
				main := readDeployFile(t, tree, "api/cmd/console/main.go")
				want := "package main\n\nimport (\n\t\"os\"\n\n\t\"github.com/stopyellingatme/spinup/kit/x/console\"\n)\n\nfunc main() {\n\tos.Exit(console.Main(os.Args[1:], os.Stdout, os.Stderr))\n}\n"
				if main != want {
					t.Errorf("console main must call only kit console.Main:\n%s", main)
				}
				dockerfile := readDeployFile(t, tree, "deploy/Dockerfile.api")
				for _, instruction := range []string{"RUN CGO_ENABLED=0 go build -trimpath -ldflags \"-s -w\" -o /out/console ./cmd/console\n", "COPY --from=build /out/console /console\n"} {
					if strings.Count(dockerfile, instruction) != 1 {
						t.Errorf("missing console instruction %q", instruction)
					}
					dockerfile = strings.Replace(dockerfile, instruction, "", 1)
				}
				prior := readDeployFile(t, renderConsole(t, mode, "absent", true, true), "deploy/Dockerfile.api")
				if dockerfile != prior {
					t.Error("console instructions changed the existing API build or kit context")
				}
			})
		}
	})
	t.Run("ExplicitOff", func(t *testing.T) {
		for _, mode := range []string{"api", "embed", "node"} {
			for _, profile := range []string{"absent", "none", "lite", "full"} {
				t.Run(mode+"/"+profile, func(t *testing.T) {
					tree := renderConsole(t, mode, profile, false, false)
					prior := renderConsole(t, mode, profile, false, true)
					if readDeployFile(t, tree, "deploy/Dockerfile.api") != readDeployFile(t, prior, "deploy/Dockerfile.api") {
						t.Error("disabled console changed Dockerfile bytes")
					}
					for _, name := range []string{"api/cmd/console/main.go", "console/compose.console.yml", "console/compose.query.yml"} {
						if _, err := fs.Stat(tree, name); !os.IsNotExist(err) {
							t.Errorf("disabled console renders %s: %v", name, err)
						}
					}
				})
			}
		}
	})
	t.Run("Manifest", func(t *testing.T) {
		pack, err := templates.Load(templates.Embedded(), "console")
		if err != nil {
			t.Fatal(err)
		}
		if pack.Description != "Operator overview of one deployed app." || !slices.Equal(pack.Requires, []string{"deploy"}) || len(pack.Files) != 3 {
			t.Fatalf("console manifest = %+v", pack)
		}
		for _, file := range pack.Files {
			if file.Class != "managed" || file.When != "" {
				t.Errorf("console file must always be managed: %+v", file)
			}
		}
		agents := readDeployFile(t, renderConsole(t, "api", "absent", true, false), "AGENTS.md")
		for _, command := range []string{"console open", "status"} {
			if !strings.Contains(agents, "`spinup "+command+" --env <env>`") || !strings.Contains(agents, render.DocsBase+"commands/"+strings.Fields(command)[0]+".md") {
				t.Errorf("missing agent row or command page for %s", command)
			}
		}
		// Keep the frozen prior source outside the generated golden subtrees.
		data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "testdata/golden/console/prior/Dockerfile.api.tmpl"))
		if err != nil || bytes.Contains(data, []byte("/out/console")) {
			t.Fatalf("invalid prior Dockerfile fixture: %v", err)
		}
	})
}
