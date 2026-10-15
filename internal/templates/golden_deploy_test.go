package templates_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stopyellingatme/spinup/internal/doctor/composelint"
	spexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/testutil"
	"go.yaml.in/yaml/v3"
)

// deployGoldenPath selects the files of the deploy pack. The api and web packs own the
// other files under deploy/.
func deployGoldenPath(name string) bool {
	switch name {
	case "deploy/compose.dev.yml", "deploy/Dockerfile.api", "deploy/Dockerfile.web":
		return false
	}
	return strings.HasPrefix(name, "deploy/")
}

// deployComposeFiles lists the Compose files that the deploy pack renders.
var deployComposeFiles = []string{"deploy/compose.yml", "deploy/compose.override.yml", "deploy/compose.edge.yml"}

func renderDeploy(t *testing.T, packs []string, answers map[string]any) (fs.FS, error) {
	t.Helper()
	all := testutil.RenderPack(t, packs, answers)
	selected := fstest.MapFS{}
	err := fs.WalkDir(all, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !deployGoldenPath(name) {
			return err
		}
		data, err := fs.ReadFile(all, name)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		selected[name] = &fstest.MapFile{Data: data, Mode: info.Mode()}
		return nil
	})
	return selected, err
}

func TestGoldenDeploy(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "deploy")
	answers := map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": "ci,deploy"}
	render := func() (fs.FS, error) {
		return renderDeploy(t, []string{"base", "api", "web", "deploy"}, answers)
	}
	fsys, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "deploy", fsys)
	testutil.Deterministic(t, render)
	checkDeployTree(t, fsys, true)

	t.Run("api-only", func(t *testing.T) {
		t.Parallel()
		fsys, err := renderDeploy(t, []string{"base", "api", "deploy"}, answers)
		if err != nil {
			t.Fatal(err)
		}
		checkDeployTree(t, fsys, false)
	})

	for _, tc := range []struct{ version, image, volume string }{
		{"17", "postgres-17", "postgres_data:/var/lib/postgresql/data\n"},
		{"16", "postgres-16", "postgres_data:/var/lib/postgresql/data\n"},
	} {
		t.Run("postgres-"+tc.version, func(t *testing.T) {
			t.Parallel()
			withVersion := map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": "ci,deploy", "db_version": tc.version}
			fsys, err := renderDeploy(t, []string{"base", "api", "web", "deploy"}, withVersion)
			if err != nil {
				t.Fatal(err)
			}
			image, err := stackversions.MustLoad().Image(tc.image)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string][]string{
				"deploy/Dockerfile.postgres": {"FROM " + image.Ref() + "\n"},
				"deploy/compose.edge.yml":    {tc.volume, "PGDATA: /var/lib/postgresql/data/pgdata", "todo-postgres:" + image.Tag + "-pgbackrest"},
			}
			for name, needles := range want {
				data := readDeployFile(t, fsys, name)
				for _, needle := range needles {
					if !strings.Contains(data, needle) {
						t.Errorf("%s does not contain %q", name, needle)
					}
				}
			}
			checkDeployTree(t, fsys, true)
		})
	}
}

func TestGoldenDeployMonitoring(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "deploy")
	for _, profile := range []string{"lite", "full", "none"} {
		t.Run(profile, func(t *testing.T) {
			t.Parallel()
			fsys, err := renderDeploy(t, []string{"base", "api", "web", "deploy", "monitoring"}, map[string]any{
				"app_name": "todo", "module": "example.com/todo", "modules": "ci,deploy,monitoring", "monitoring_profile": profile,
			})
			if err != nil {
				t.Fatal(err)
			}
			compose := readDeployFile(t, fsys, "deploy/compose.yml")
			want := 2
			if profile == "none" {
				want = 0
			}
			if got := strings.Count(compose, "OTEL_EXPORTER_OTLP_ENDPOINT: http://alloy:4318"); got != want {
				t.Errorf("OTLP endpoints = %d, want %d", got, want)
			}
			if got := strings.Count(compose, "OTEL_RESOURCE_ATTRIBUTES: service.instance.id=${SPINUP_COLOR}"); got != want {
				t.Errorf("color resource attributes = %d, want %d", got, want)
			}
			for _, service := range []string{"api", "web"} {
				if got := strings.Contains(compose, "OTEL_SERVICE_NAME: todo-"+service); got != (profile != "none") {
					t.Errorf("service name %s present = %v", service, got)
				}
			}
			checkDeployTree(t, fsys, true)
		})
	}
}

// checkDeployTree checks the contracts of ADR 0003 and ADR 0009 §6 that a golden diff does
// not explain: the Compose security lint, the blue/green aliases, the routes, and the pins.
func checkDeployTree(t *testing.T, fsys fs.FS, web bool) {
	t.Helper()
	for _, name := range deployComposeFiles {
		findings, err := composelint.Lint(name, []byte(readDeployFile(t, fsys, name)))
		if err != nil {
			t.Fatalf("lint %s: %v", name, err)
		}
		for _, finding := range findings {
			t.Errorf("%s: %s: %s: %s", finding.File, finding.Service, finding.Rule, finding.Message)
		}
	}

	compose := readDeployFile(t, fsys, "deploy/compose.yml")
	edge := readDeployFile(t, fsys, "deploy/compose.edge.yml")
	caddyfile := readDeployFile(t, fsys, "deploy/Caddyfile")
	stack := stackversions.MustLoad()
	caddy, err := stack.Image("caddy")
	if err != nil {
		t.Fatal(err)
	}
	contains := map[string][]string{
		"deploy/compose.yml": {
			"name: todo-${SPINUP_COLOR:?",
			"include:\n  - compose.override.yml\n",
			"- api-${SPINUP_COLOR}\n",
			"SPINUP_ENV: production\n",
			"PUBLIC_ORIGIN: ${PUBLIC_ORIGIN:?",
			"DATABASE_URL_FILE: /run/secrets/DATABASE_URL\n",
			"- /opt/todo/secrets:/run/secrets:ro\n",
			"profiles: [migrate]\n",
			`command: ["migrate", "up"]`,
			"name: todo-app\n    external: true\n",
			"name: todo-db\n    external: true\n",
		},
		"deploy/compose.edge.yml": {
			"name: todo\n",
			"image: " + caddy.Ref() + "\n",
			"spinup.public: \"true\"\n",
			"- /opt/todo/caddy:/etc/caddy:ro\n",
			"POSTGRES_PASSWORD_FILE: /run/secrets/POSTGRES_PASSWORD\n",
			"name: todo-db\n    internal: true\n",
		},
		"deploy/Caddyfile": {
			"admin localhost:2019\n",
			"grace_period 10s\n",
			"{$PUBLIC_HOST} {\n",
			"encode zstd gzip\n",
			"import Caddyfile.d/*.caddy\n",
			"import upstream.caddy api 8080\n",
		},
		"deploy/Dockerfile.postgres": {
			"ARG PGBACKREST_VERSION=" + stack.Apt["pgbackrest"] + "\n",
			`"pgbackrest=${PGBACKREST_VERSION}-*"`,
		},
		"deploy/cloud-init/user-data.yaml": {
			"#cloud-config\n",
			"/opt/todo/releases",
			"/opt/todo/secrets]",
			"[ufw, allow, 443/tcp]",
		},
	}
	if web {
		contains["deploy/compose.yml"] = append(contains["deploy/compose.yml"], "- web-${SPINUP_COLOR}\n", "API_URL: http://api-${SPINUP_COLOR}:8080\n")
		contains["deploy/Caddyfile"] = append(contains["deploy/Caddyfile"], "handle /api/* {\n", "import upstream.caddy web 3000\n")
	} else {
		for _, absent := range []string{"WEB_IMAGE", "web-${SPINUP_COLOR}"} {
			if strings.Contains(compose, absent) {
				t.Errorf("deploy/compose.yml of an API-only app contains %q", absent)
			}
		}
		if strings.Contains(caddyfile, "handle /api/*") || strings.Contains(caddyfile, "upstream.caddy web") {
			t.Error("deploy/Caddyfile of an API-only app routes to the web service")
		}
	}
	for name, needles := range contains {
		data := readDeployFile(t, fsys, name)
		for _, needle := range needles {
			if !strings.Contains(data, needle) {
				t.Errorf("%s does not contain %q", name, needle)
			}
		}
	}
	// ADR 0003: the plain service name resolves to both colors on the shared network.
	if strings.Contains(compose, "http://api:8080") {
		t.Error("deploy/compose.yml uses http://api:8080; use the color alias api-${SPINUP_COLOR}")
	}
	// Only Caddy publishes ports. Postgres publishes none.
	if strings.Contains(compose, "ports:") || strings.Count(edge, "ports:") != 1 {
		t.Error("a service other than caddy publishes a port")
	}
	for _, name := range []string{"deploy/compose.yml", "deploy/compose.edge.yml"} {
		if strings.Contains(readDeployFile(t, fsys, name), ":latest") {
			t.Errorf("%s uses a latest tag", name)
		}
	}

	dir := writeDeployTree(t, fsys)
	composeConfig(t, dir, "compose.yml", "SPINUP_COLOR=blue", "API_IMAGE=todo-api:20261015T000000Z-0123456789ab",
		"WEB_IMAGE=todo-web:20261015T000000Z-0123456789ab", "PUBLIC_ORIGIN=https://todo.example.com")
	composeConfig(t, dir, "compose.edge.yml", "PUBLIC_HOST=todo.example.com")
	caddyAdapt(t, dir, web)
}

func readDeployFile(t *testing.T, fsys fs.FS, name string) string {
	t.Helper()
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeDeployTree(t *testing.T, fsys fs.FS) string {
	t.Helper()
	root := t.TempDir()
	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "deploy")
}

// composeConfig validates a rendered Compose file with `docker compose config -q`.
// The command needs the docker CLI with the Compose plugin, but no daemon.
func composeConfig(t *testing.T, dir, file string, env ...string) {
	t.Helper()
	if _, err := spexec.LookPath("docker"); err != nil {
		t.Logf("skip docker compose config %s: docker is not on PATH", file)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result, err := spexec.OSRunner{}.Run(ctx, spexec.Cmd{
		Name: "docker",
		Args: []string{"compose", "-f", file, "config", "-q"},
		Dir:  dir,
		Env:  append([]string{"DOCKER_CLI_HINTS=false"}, env...),
	})
	if err != nil {
		t.Errorf("docker compose -f %s config -q: %v\n%s%s", file, err, result.Stdout, result.Stderr)
	}
}

// caddyAdapt parses the rendered Caddyfile with a sample upstream.caddy when caddy is on PATH.
func caddyAdapt(t *testing.T, dir string, web bool) {
	t.Helper()
	if _, err := spexec.LookPath("caddy"); err != nil {
		t.Logf("skip caddy adapt: caddy is not on PATH")
		return
	}
	upstream := "reverse_proxy {\n\tdynamic a {\n\t\tname {args[0]}-green\n\t\tport {args[1]}\n\t\trefresh 1s\n\t}\n" +
		"\tlb_policy round_robin\n\tlb_try_duration 5s\n\tlb_try_interval 50ms\n" +
		"\ttransport http {\n\t\tdial_timeout 250ms\n\t}\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "upstream.caddy"), []byte(upstream), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result, err := spexec.OSRunner{}.Run(ctx, spexec.Cmd{
		Name: "caddy",
		Args: []string{"adapt", "--config", "Caddyfile", "--adapter", "caddyfile"},
		Dir:  dir,
		Env:  []string{"PUBLIC_HOST=todo.example.com"},
	})
	if err != nil {
		t.Fatalf("caddy adapt: %v\n%s", err, result.Stderr)
	}
	config := string(result.Stdout)
	for _, want := range []string{`"name":"api-green"`, `"port":"8080"`, `"dial_timeout":250000000`, `"grace_period":10000000000`} {
		if !strings.Contains(config, want) {
			t.Errorf("caddy adapt output does not contain %s", want)
		}
	}
	if got := strings.Contains(config, `"name":"web-green"`); got != web {
		t.Errorf("caddy adapt routes to web-green = %v, want %v", got, web)
	}
}

func TestDeployIgnoresUnselectedMonitoring(t *testing.T) {
	t.Parallel()
	fsys, err := renderDeploy(t, []string{"base", "api", "web", "deploy"}, map[string]any{
		"app_name": "todo", "module": "example.com/todo", "modules": "ci,deploy", "monitoring_profile": "full",
	})
	if err != nil {
		t.Fatal(err)
	}
	if compose := readDeployFile(t, fsys, "deploy/compose.yml"); strings.Contains(compose, "OTEL_") {
		t.Errorf("unselected monitoring sets the environment:\n%s", compose)
	}
}

func TestMonitoringConfigLabelsAndSecrets(t *testing.T) {
	t.Parallel()
	for _, profile := range []string{"lite", "full"} {
		t.Run(profile, func(t *testing.T) {
			t.Parallel()
			fsys := renderMonitoring(t, profile)
			var compose struct {
				Services map[string]struct {
					Labels      map[string]string
					Volumes     []string
					Environment map[string]string
				}
			}
			if err := yaml.Unmarshal([]byte(readDeployFile(t, fsys, "monitoring/compose.monitoring.yml")), &compose); err != nil {
				t.Fatal(err)
			}
			for service, key := range map[string]string{
				"alloy": "ALLOY", "prometheus": "PROMETHEUS", "loki": "LOKI", "tempo": "TEMPO", "grafana": "GRAFANA", "blackbox-exporter": "BLACKBOX",
			} {
				if cfg, ok := compose.Services[service]; ok && cfg.Labels["spinup.config-hash"] != "${"+key+"_CONFIG_HASH:-}" {
					t.Errorf("%s config hash label = %v", service, cfg.Labels)
				}
			}
			for service, cfg := range compose.Services {
				for _, mount := range cfg.Volumes {
					if strings.HasPrefix(mount, "/opt/todo/secrets:") {
						t.Errorf("%s mounts all application secrets", service)
					}
				}
				if service == "grafana" || service == "postgres-exporter" {
					found := false
					for _, mount := range cfg.Volumes {
						if mount == "/opt/todo/monitoring-secrets:/run/secrets:ro" {
							found = true
						}
					}
					if !found {
						t.Errorf("%s misses restricted secrets mount", service)
					}
				}
			}
			if profile == "full" && compose.Services["postgres-exporter"].Environment["DATA_SOURCE_PASS_FILE"] != "/run/secrets/POSTGRES_PASSWORD" {
				t.Error("postgres-exporter uses the wrong secret")
			}
		})
	}
}
