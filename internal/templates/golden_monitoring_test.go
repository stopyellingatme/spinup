package templates_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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

	// Profile none renders no monitoring file.
	t.Run("none", func(t *testing.T) {
		t.Parallel()
		fsys := renderMonitoring(t, "none")
		err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
			if err == nil && !entry.IsDir() {
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
		// spinup.toml repeats the profile answer, and the question default is lite.
		data, err := fs.ReadFile(all, "spinup.toml")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "[monitoring]\n# Pick the monitoring stack: lite, full, or none.\nprofile = \"lite\"\n") {
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
	// The vendored dashboards need the full exporters, so only the full profile renders them.
	if want := map[bool]int{false: 5, true: 8}[full]; len(dashboards) < want {
		t.Errorf("dashboards = %v, want spinup's five (plus the three vendored ones in full)", dashboards)
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

// TestMonitoringBodyRedaction applies the rendered OTTL regexes in their export order.
func TestMonitoringBodyRedaction(t *testing.T) {
	t.Parallel()
	alloy := readDeployFile(t, renderMonitoring(t, "lite"), "monitoring/alloy/config.alloy")
	// OTTL and Go use the same quoted string escapes and RE2 regex syntax.
	statements := regexp.MustCompile(`replace_pattern\(body, ("(?:\\.|[^"\\])*"), ("(?:\\.|[^"\\])*")\)`).FindAllStringSubmatch(alloy, -1)
	if len(statements) == 0 {
		t.Fatal("no log-body replacements found")
	}
	for _, tc := range []struct{ name, body, secret string }{
		{"header", "request failed Authorization: Bearer abcdef123456.xyz", "abcdef123456.xyz"},
		{"logfmt", `authorization="Bearer abcdef123456xyz"`, "abcdef123456xyz"},
		{"basic", "authorization=Basic dXNlcjpwYXNz", "dXNlcjpwYXNz"},
		{"short credential", "Authorization: Bearer short", "short"},
		{"JSON spaces", `{"password":"hunter2 and more","message":"keep me"}`, "hunter2 and more"},
		{"single quotes", "password='hunter2 and more' message=keep", "hunter2 and more"},
		{"escaped quote", `{"password":"hunter2 \" and more"}`, "and more"},
		{"ordinary text", "request completed successfully", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.body
			for _, statement := range statements {
				pattern, err := strconv.Unquote(statement[1])
				if err != nil {
					t.Fatal(err)
				}
				replacement, err := strconv.Unquote(statement[2])
				if err != nil {
					t.Fatal(err)
				}
				got = regexp.MustCompile(pattern).ReplaceAllString(got, replacement)
			}
			if tc.secret == "" {
				if got != tc.body {
					t.Errorf("ordinary text changed: %q", got)
				}
				return
			}
			for _, word := range strings.Fields(tc.secret) {
				if strings.Contains(got, word) {
					t.Errorf("secret remains in %q", got)
				}
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Errorf("no redaction in %q", got)
			}
			if strings.Contains(tc.body, "keep me") && !strings.Contains(got, "keep me") {
				t.Errorf("nonsecret field removed: %q", got)
			}
		})
	}
}

func TestMonitoringDashboardCoverage(t *testing.T) {
	t.Parallel()
	for _, profile := range []string{"lite", "full"} {
		t.Run(profile, func(t *testing.T) {
			t.Parallel()
			fsys := renderMonitoring(t, profile)
			runtime := readDeployFile(t, fsys, "monitoring/grafana/dashboards/stack/go-runtime.json")
			if strings.Contains(runtime, "process_cpu_seconds_total") {
				t.Error("runtime dashboard queries a CPU metric the API does not produce")
			}
			backups := readDeployFile(t, fsys, "monitoring/grafana/dashboards/stack/backups.json")
			if got := strings.Contains(backups, "pg_stat_archiver_"); got != (profile == "full") {
				t.Errorf("WAL panels present = %v in profile %s", got, profile)
			}
			if !strings.Contains(backups, "backup_last_age_seconds") {
				t.Error("backup age panel missing")
			}
			app := readDeployFile(t, fsys, "monitoring/prometheus/rules/app.yml")
			if strings.Contains(app, "<app>-web") || !strings.Contains(app, "API") {
				t.Error("HTTP RED must describe API coverage")
			}
		})
	}
}

func TestMonitoringAlloyJob(t *testing.T) {
	t.Parallel()
	alloy := readDeployFile(t, renderMonitoring(t, "lite"), "monitoring/alloy/config.alloy")
	// The self exporter supplies job=integrations/self, which overrides job_name.
	if !strings.Contains(alloy, `replacement = "alloy"`) || !strings.Contains(alloy, `target_label = "job"`) {
		t.Error("self scrape must replace the exporter job label with alloy")
	}
}

func TestMonitoringAbsenceAlerts(t *testing.T) {
	t.Parallel()
	for _, profile := range []string{"lite", "full"} {
		t.Run(profile, func(t *testing.T) {
			t.Parallel()
			fsys := renderMonitoring(t, profile)
			dir := writeMonitoringTree(t, fsys)
			// A healthy stack stays quiet. Missing and stale data fire the absence alerts.
			tests := `rule_files:
  - monitoring/prometheus/rules/stack.yml
  - monitoring/prometheus/rules/app.yml
evaluation_interval: 1m
tests:
  - interval: 1m
    input_series:
      - series: 'up{job="alloy",instance="self"}'
        values: '1+0x20'
      - series: 'http_server_request_duration_seconds_count{job="todo-api"}'
        values: '0+1x20'
    alert_rule_test:
      - eval_time: 15m
        alertname: AlloyMissing
        exp_alerts: []
      - eval_time: 15m
        alertname: APIRequestMetricsMissing
        exp_alerts: []
  - interval: 1m
    input_series:
      - series: 'up{job="alloy",instance="self"}'
        values: '1 1 stale _x15'
      - series: 'http_server_request_duration_seconds_count{job="todo-api"}'
        values: '1 2 stale _x15'
    promql_expr_test:
      - expr: 'ALERTS{alertname="AlloyMissing",alertstate="firing"}'
        eval_time: 4m
        exp_samples: []
      - expr: 'ALERTS{alertname="AlloyMissing",alertstate="firing"}'
        eval_time: 8m
        exp_samples:
          - labels: 'ALERTS{alertname="AlloyMissing",alertstate="firing",job="alloy",severity="critical"}'
            value: 1
      - expr: 'ALERTS{alertname="APIRequestMetricsMissing",alertstate="firing"}'
        eval_time: 13m
        exp_samples:
          - labels: 'ALERTS{alertname="APIRequestMetricsMissing",alertstate="firing",severity="warning"}'
            value: 1
  - interval: 1m
    promql_expr_test:
      - expr: 'ALERTS{alertname=~"AlloyMissing|APIRequestMetricsMissing",alertstate="firing"}'
        eval_time: 15m
        exp_samples:
          - labels: 'ALERTS{alertname="AlloyMissing",alertstate="firing",job="alloy",severity="critical"}'
            value: 1
          - labels: 'ALERTS{alertname="APIRequestMetricsMissing",alertstate="firing",severity="warning"}'
            value: 1
`
			if err := os.WriteFile(filepath.Join(dir, "absence.yml"), []byte(tests), 0o600); err != nil {
				t.Fatal(err)
			}
			promtool(t, dir, "test", "rules", "absence.yml")
		})
	}
}

func TestMonitoringGrafanaPassword(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                  string
		supported, resetFails bool
	}{
		{name: "rotate", supported: true},
		{name: "reset failure", supported: true, resetFails: true},
		{name: "manual fallback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			const password = `new ' " $(touch injected) ` + "`touch injected`" + ` ; $HOME \\ password`
			files := map[string]string{
				"secrets/GRAFANA_ADMIN_PASSWORD": password,
				"source/.keep":                   "",
				"bin/wget": `#!/bin/sh
if [ -f "$TEST_ROOT/started" ]; then
 touch "$TEST_ROOT/healthy"
 exit 0
fi
exit 1
`,
				"bin/grafana": `#!/bin/sh
for arg do
 if [ "$arg" = --help ]; then
  if [ "$SUPPORTED" = true ]; then echo --password-from-stdin; fi
  exit 0
 fi
done
if [ ! -f "$TEST_ROOT/healthy" ]; then exit 2; fi
printf '%s\n' "$@" > "$TEST_ROOT/args"
IFS= read -r password
printf '%s' "$password" > "$TEST_ROOT/password"
if [ "$RESET_FAILS" = true ]; then exit 1; fi
touch "$TEST_ROOT/reset"
`,
				"server.sh": `#!/bin/sh
trap 'exit 0' TERM INT
touch "$TEST_ROOT/started"
# A reset failure must stop the server. The outer test timeout catches a missing stop.
if [ "$RESET_FAILS" = true ]; then
 while :; do sleep 0.1; done
fi
if [ "$SUPPORTED" = true ]; then
 while [ ! -f "$TEST_ROOT/reset" ]; do sleep 0.1; done
fi
exit 0
`,
			}
			for name, data := range files {
				target := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := spexec.WriteExecutable(target, []byte(data), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			script := readDeployFile(t, renderMonitoring(t, "lite"), "monitoring/grafana/entrypoint.sh")
			script = strings.NewReplacer("/run/secrets", filepath.Join(root, "secrets"), "/etc/spinup/provisioning", filepath.Join(root, "source"), "/run.sh", filepath.Join(root, "server.sh")).Replace(script)
			scriptPath := filepath.Join(root, "entrypoint.sh")
			if err := spexec.WriteExecutable(scriptPath, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			result, err := spexec.OSRunner{}.Run(t.Context(), spexec.Cmd{
				Name: "sh", Args: []string{scriptPath}, Dir: root, Timeout: 5 * time.Second,
				Env: []string{"TEST_ROOT=" + root, "PATH=" + filepath.Join(root, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"), "GF_PATHS_PROVISIONING=" + filepath.Join(root, "provisioning"), "SUPPORTED=" + strconv.FormatBool(tc.supported), "RESET_FAILS=" + strconv.FormatBool(tc.resetFails)},
			})
			if tc.resetFails {
				if err == nil {
					t.Fatal("Grafana started despite a failed password reset")
				}
				if !strings.Contains(string(result.Stderr), "Try:") {
					t.Errorf("reset failure has no guidance: %s", result.Stderr)
				}
			} else if err != nil {
				t.Fatalf("start Grafana: %v\n%s%s", err, result.Stdout, result.Stderr)
			}
			if tc.supported {
				got, readErr := os.ReadFile(filepath.Join(root, "password"))
				if readErr != nil {
					t.Fatal(readErr)
				}
				if string(got) != password {
					t.Errorf("password changed during stdin transfer: %q", got)
				}
				args, readErr := os.ReadFile(filepath.Join(root, "args"))
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !strings.Contains(string(args), "--password-from-stdin") || strings.Contains(string(args), password) {
					t.Errorf("password must enter the CLI through stdin: %s", args)
				}
				if !strings.Contains(string(args), "cfg:default.paths.data=") {
					t.Errorf("CLI must use the server database: %s", args)
				}
			} else if !strings.Contains(string(result.Stderr), "manual reset") {
				t.Errorf("unsupported CLI needs manual reset guidance: %s", result.Stderr)
			}
			if _, err := os.Stat(filepath.Join(root, "injected")); !os.IsNotExist(err) {
				t.Fatal("password executed as shell code")
			}
			if strings.Contains(string(result.Stdout)+string(result.Stderr), password) {
				t.Fatal("password leaked in startup output")
			}
		})
	}
}
