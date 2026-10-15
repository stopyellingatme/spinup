package templates_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
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

// monitoringVendorDir holds the vendored dashboards of the main-loop fragment pack.d/vendor.toml.
const monitoringVendorDir = "monitoring/grafana/dashboards/stack/vendor/"

func renderMonitoring(t *testing.T, profile string) fs.FS {
	t.Helper()
	all := testutil.RenderPack(t, []string{"base", "api", "web", "deploy", "monitoring"}, map[string]any{
		"app_name": "todo", "module": "example.com/todo", "modules": "ci,deploy,monitoring", "monitoring_profile": profile,
	})
	selected := fstest.MapFS{}
	err := fs.WalkDir(all, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasPrefix(name, "monitoring/") {
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
	if err != nil {
		t.Fatal(err)
	}
	return selected
}

func TestGoldenMonitoring(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "monitoring")
	for _, profile := range []string{"lite", "full"} {
		t.Run(profile, func(t *testing.T) {
			t.Parallel()
			render := func() (fs.FS, error) { return renderMonitoring(t, profile), nil }
			fsys, _ := render()
			testutil.Golden(t, "monitoring/"+profile, fsys)
			testutil.Deterministic(t, render)
			checkMonitoringTree(t, fsys, profile == "full")
		})
	}

	// Profile none renders no monitoring file. The vendored dashboards come from the main-loop
	// fragment pack.d/vendor.toml, which has no profile condition yet.
	t.Run("none", func(t *testing.T) {
		t.Parallel()
		fsys := renderMonitoring(t, "none")
		err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
			if err == nil && !entry.IsDir() && !strings.HasPrefix(name, monitoringVendorDir) {
				t.Errorf("profile none renders %s", name)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("spinup.toml", func(t *testing.T) {
		t.Parallel()
		all := testutil.RenderPack(t, []string{"base", "api", "deploy", "monitoring"}, map[string]any{
			"app_name": "todo", "module": "example.com/todo", "modules": "ci,deploy,monitoring",
		})
		// The question default and the base pack's literal [monitoring] profile agree.
		data, err := fs.ReadFile(all, "spinup.toml")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "[monitoring]\n# Use the small monitoring stack.\nprofile = \"lite\"\n") {
			t.Errorf("spinup.toml does not select the lite profile:\n%s", data)
		}
		if _, err := fs.Stat(all, "monitoring/compose.monitoring.yml"); err != nil {
			t.Errorf("the default profile renders no compose.monitoring.yml: %v", err)
		}
	})
}

// checkMonitoringTree checks the contracts of ADR 0010 §2 that a golden diff does not explain:
// the Compose security lint, the pins, the profile services, the datasource UIDs, the rule
// files, and the dashboard JSON.
func checkMonitoringTree(t *testing.T, fsys fs.FS, full bool) {
	t.Helper()
	const composeFile = "monitoring/compose.monitoring.yml"
	compose := readDeployFile(t, fsys, composeFile)
	findings, err := composelint.Lint(composeFile, []byte(compose))
	if err != nil {
		t.Fatalf("lint %s: %v", composeFile, err)
	}
	for _, finding := range findings {
		t.Errorf("%s: %s: %s: %s", finding.File, finding.Service, finding.Rule, finding.Message)
	}

	var parsed struct {
		Name     string `yaml:"name"`
		Services map[string]struct {
			Image       string         `yaml:"image"`
			Ports       []string       `yaml:"ports"`
			ReadOnly    bool           `yaml:"read_only"`
			CapDrop     []string       `yaml:"cap_drop"`
			CapAdd      []string       `yaml:"cap_add"`
			SecurityOpt []string       `yaml:"security_opt"`
			Healthcheck map[string]any `yaml:"healthcheck"`
			Logging     map[string]any `yaml:"logging"`
			Networks    map[string]any `yaml:"networks"`
		} `yaml:"services"`
		Networks map[string]struct {
			Name     string `yaml:"name"`
			External bool   `yaml:"external"`
			Internal bool   `yaml:"internal"`
		} `yaml:"networks"`
	}
	if err := yaml.Unmarshal([]byte(compose), &parsed); err != nil {
		t.Fatalf("parse %s: %v", composeFile, err)
	}
	if parsed.Name != "todo-monitoring" {
		t.Errorf("project name = %q, want todo-monitoring", parsed.Name)
	}
	want := []string{"alloy", "grafana", "loki", "prometheus"}
	if full {
		want = append(want, "blackbox-exporter", "cadvisor", "node-exporter", "postgres-exporter", "tempo")
	}
	slices.Sort(want)
	var got []string
	for name := range parsed.Services {
		got = append(got, name)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("services = %v, want %v", got, want)
	}
	stack := stackversions.MustLoad()
	for name, service := range parsed.Services {
		image, err := stack.Image(name)
		if err != nil {
			t.Fatalf("service %s has no pinned image: %v", name, err)
		}
		if service.Image != image.Ref() {
			t.Errorf("service %s image = %q, want %q", name, service.Image, image.Ref())
		}
		if !service.ReadOnly || !slices.Equal(service.CapDrop, []string{"ALL"}) || len(service.CapAdd) != 0 ||
			!slices.Contains(service.SecurityOpt, "no-new-privileges:true") || service.Logging == nil {
			t.Errorf("service %s lost its hardening: read_only=%v cap_drop=%v cap_add=%v security_opt=%v", name, service.ReadOnly, service.CapDrop, service.CapAdd, service.SecurityOpt)
		}
		// The Loki image has no shell and no HTTP client (see the comment in the file).
		if service.Healthcheck == nil && name != "loki" {
			t.Errorf("service %s has no health check", name)
		}
		wantPorts := []string(nil)
		if name == "grafana" {
			wantPorts = []string{"127.0.0.1:3000:3000"}
		}
		if !slices.Equal(service.Ports, wantPorts) {
			t.Errorf("service %s ports = %v, want %v", name, service.Ports, wantPorts)
		}
	}
	if _, ok := parsed.Services["alloy"].Networks["app"]; !ok {
		t.Error("alloy is not on the app network: the app cannot reach alloy:4318")
	}
	if n := parsed.Networks["app"]; n.Name != "todo-app" || !n.External {
		t.Errorf("app network = %+v, want the external todo-app", n)
	}
	if n := parsed.Networks["monitoring"]; !n.Internal {
		t.Errorf("monitoring network = %+v, want internal", n)
	}
	if n, ok := parsed.Networks["db"]; ok != full || ok && (n.Name != "todo-db" || !n.External) {
		t.Errorf("db network = %+v (present %v), want the external todo-db only in profile full", n, ok)
	}

	alloy := readDeployFile(t, fsys, "monitoring/alloy/config.alloy")
	for _, needle := range []string{`endpoint = "0.0.0.0:4317"`, `endpoint = "0.0.0.0:4318"`, `otelcol.processor.transform "redact"`, "delete_matching_keys(", "[REDACTED]",
		"http://prometheus:9090/api/v1/write", "http://loki:3100/otlp"} {
		if !strings.Contains(alloy, needle) {
			t.Errorf("config.alloy does not contain %q", needle)
		}
	}
	if got := strings.Contains(alloy, "http://tempo:4318"); got != full {
		t.Errorf("config.alloy exports to Tempo = %v, want %v", got, full)
	}

	var datasources struct {
		Datasources []struct {
			UID  string `yaml:"uid"`
			Type string `yaml:"type"`
		} `yaml:"datasources"`
	}
	if err := yaml.Unmarshal([]byte(readDeployFile(t, fsys, "monitoring/grafana/provisioning/datasources/datasources.yml")), &datasources); err != nil {
		t.Fatal(err)
	}
	var uids []string
	for _, ds := range datasources.Datasources {
		if ds.UID != ds.Type {
			t.Errorf("datasource %s has the UID %q, want %q", ds.Type, ds.UID, ds.Type)
		}
		uids = append(uids, ds.UID)
	}
	wantUIDs := []string{"prometheus", "loki"}
	if full {
		wantUIDs = append(wantUIDs, "tempo")
	}
	if !slices.Equal(uids, wantUIDs) {
		t.Errorf("datasource UIDs = %v, want %v", uids, wantUIDs)
	}

	// Every dashboard is valid JSON, and spinup's dashboards use the fixed datasource UIDs.
	var ruleFiles, dashboards []string
	err = fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		switch {
		case strings.HasPrefix(name, "monitoring/grafana/dashboards/") && path.Ext(name) == ".json":
			dashboards = append(dashboards, name)
		case strings.HasPrefix(name, "monitoring/prometheus/rules/"), strings.HasPrefix(name, "monitoring/alerts/"):
			ruleFiles = append(ruleFiles, name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboards) < 8 {
		t.Errorf("dashboards = %v, want spinup's five and the three vendored ones", dashboards)
	}
	for _, name := range dashboards {
		var dashboard struct {
			UID    string `json:"uid"`
			Title  string `json:"title"`
			Panels []any  `json:"panels"`
		}
		data := readDeployFile(t, fsys, name)
		if err := json.Unmarshal([]byte(data), &dashboard); err != nil {
			t.Errorf("%s is not valid JSON: %v", name, err)
			continue
		}
		if dashboard.UID == "" || dashboard.Title == "" || len(dashboard.Panels) == 0 {
			t.Errorf("%s has no uid, title, or panels", name)
		}
		if strings.Contains(data, "${DS_") {
			t.Errorf("%s uses a datasource input; use the provisioned UID", name)
		}
	}
	wantRules := []string{"monitoring/alerts/custom.yml", "monitoring/prometheus/rules/app.yml", "monitoring/prometheus/rules/stack.yml"}
	if full {
		wantRules = append(wantRules, "monitoring/prometheus/rules/certs.yml", "monitoring/prometheus/rules/host.yml", "monitoring/prometheus/rules/postgres.yml")
	}
	slices.Sort(wantRules)
	slices.Sort(ruleFiles)
	if !slices.Equal(ruleFiles, wantRules) {
		t.Errorf("rule files = %v, want %v", ruleFiles, wantRules)
	}

	dir := writeMonitoringTree(t, fsys)
	promtool(t, dir, append([]string{"check", "rules"}, ruleFiles...)...)
	promtool(t, dir, "check", "config", "monitoring/prometheus/prometheus.yml")
	composeConfig(t, filepath.Join(dir, "monitoring"), "compose.monitoring.yml", "PUBLIC_URL=https://todo.example.com")
}

func writeMonitoringTree(t *testing.T, fsys fs.FS) string {
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
	return root
}

// promtool runs promtool in dir when it is on PATH.
func promtool(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := spexec.LookPath("promtool"); err != nil {
		t.Logf("skip promtool %s: promtool is not on PATH", strings.Join(args[:2], " "))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result, err := spexec.OSRunner{}.Run(ctx, spexec.Cmd{Name: "promtool", Args: args, Dir: dir})
	if err != nil {
		t.Errorf("promtool %s: %v\n%s%s", strings.Join(args, " "), err, result.Stdout, result.Stderr)
	}
}
