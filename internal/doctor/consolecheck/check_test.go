package consolecheck

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/render"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
)

func fixture(t *testing.T, profile string) *doctor.Context {
	t.Helper()
	root := t.TempDir()
	names := []string{"base", "api", "deploy", "console"}
	mods := "deploy,console"
	answers := map[string]any{"app_name": "todo", "module": "example.com/todo", "frontend_mode": "node"}
	if profile != "absent" {
		names = append(names, "monitoring")
		mods += ",monitoring"
		answers["monitoring_profile"] = profile
	}
	answers["modules"] = mods
	var packs []*templates.Pack
	for _, name := range names {
		p, err := templates.Load(templates.Embedded(), name)
		if err != nil {
			t.Fatal(err)
		}
		packs = append(packs, p)
	}
	plan, err := render.Render(render.Input{Packs: packs, Answers: answers, Stack: stackversions.MustLoad(), TemplateVersion: "0.0.0-test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range plan.Files {
		write(t, root, file.Path, string(file.Data))
	}
	return &doctor.Context{Root: root, Env: "production"}
}

func write(t *testing.T, root, path, data string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestConsoleWired(t *testing.T) {
	for _, profile := range []string{"absent", "none", "lite", "full"} {
		t.Run(profile, func(t *testing.T) {
			c := fixture(t, profile)
			if got := localCheck(context.Background(), c); got.Status != doctor.OK || got.Message != "Console wiring is present." {
				t.Fatalf("%+v", got)
			}
		})
	}

	t.Run("equivalent Compose syntax", func(t *testing.T) {
		c := fixture(t, "lite")
		b, err := os.ReadFile(filepath.Join(c.Root, basePath))
		if err != nil {
			t.Fatal(err)
		}
		source := strings.Replace(string(b), `- "127.0.0.1:7410:7410/tcp"`, `- {target: 7410, published: "7410", host_ip: "127.0.0.1", protocol: tcp}`, 1)
		source = strings.Replace(source, `- /opt/todo/console/data:/data:ro`, `- {type: bind, source: /opt/todo/console/data, target: /data, read_only: true}`, 1)
		source = strings.Replace(source, `networks: [console]`, `networks: {console: {}}`, 1)
		write(t, c.Root, basePath, source)
		if got := localCheck(context.Background(), c); got.Status != doctor.OK {
			t.Fatal(got)
		}
	})
	for _, tc := range []struct{ name, path, old, new string }{
		{"comment is not wiring", "api/cmd/console/main.go", "os.Exit(console.Main(os.Args[1:], os.Stdout, os.Stderr))", "// os.Exit(console.Main(os.Args[1:], os.Stdout, os.Stderr))"},
		{"dead function", "api/cmd/console/main.go", "func main()", "func ignored()"},
		{"wrong arguments", "api/cmd/console/main.go", "os.Args[1:]", "os.Args[2:]"},
		{"extra startup", "api/cmd/console/main.go", "func main() {", "func main() { panic(1);"},
		{"missing build", "deploy/Dockerfile.api", "RUN CGO_ENABLED=0 go build -trimpath -ldflags \"-s -w\" -o /out/console ./cmd/console", "# RUN CGO_ENABLED=0 go build -trimpath -ldflags \"-s -w\" -o /out/console ./cmd/console"},
		{"missing copy", "deploy/Dockerfile.api", "COPY --from=build /out/console /console", "# COPY --from=build /out/console /console"},
		{"wrong build source", "deploy/Dockerfile.api", "-o /out/console ./cmd/console", "-o /out/console ./cmd/api"},
		{"public binding", "console/compose.console.yml", "127.0.0.1:7410", "0.0.0.0:7410"},
		{"extra binding", "console/compose.console.yml", "    volumes:", "      - '0.0.0.0:9999:7410'\n    volumes:"},
		{"socket mount", "console/compose.console.yml", "    networks:", "      - /var/run/docker.sock:/var/run/docker.sock:ro\n    networks:"},
		{"host namespace", "console/compose.console.yml", "    read_only: true", "    read_only: true\n    pid: host"},
		{"privileged", "console/compose.console.yml", "    read_only: true", "    read_only: true\n    privileged: true"},
		{"credential", "console/compose.console.yml", "      GOMEMLIMIT: 40MiB", "      GOMEMLIMIT: 40MiB\n      DATABASE_URL: postgres://secret"},
		{"write mount", "console/compose.console.yml", ":/data:ro", ":/data:rw"},
		{"wrong health probe", "console/compose.console.yml", "\"healthcheck\"", "\"project\""},
		{"source health probe", "console/compose.console.yml", "interval: 15s", "interval: 30s"},
		{"heap limit", "console/compose.console.yml", "40MiB", "400MiB"},
		{"relay publication", "console/compose.query.yml", "    profiles:", "    ports: ['7411:7411']\n    profiles:"},
		{"relay mount", "console/compose.query.yml", "    profiles:", "    volumes: ['/etc:/host:ro']\n    profiles:"},
		{"relay profile", "console/compose.query.yml", "profiles: [query]", "profiles: [default]"},
		{"page on monitoring", "console/compose.console.yml", "networks: [console]", "networks: [console, monitoring]"},
		{"extra service", "console/compose.console.yml", "services:", "services:\n  intruder:\n    image: busybox\n    networks: [console]"},
		{"yaml merge attack", "console/compose.console.yml", "    read_only: true", "    <<: {privileged: true}\n    read_only: true"},
		{"include attack", "console/compose.console.yml", "services:", "include: [unsafe.yml]\nservices:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixture(t, "lite")
			data, err := os.ReadFile(filepath.Join(c.Root, tc.path))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), tc.old) {
				t.Fatalf("fixture has no %q", tc.old)
			}
			write(t, c.Root, tc.path, strings.Replace(string(data), tc.old, tc.new, 1))
			got := localCheck(context.Background(), c)
			if got.Status != doctor.Fail || got.Message != "Console wiring is unsafe or incomplete: "+tc.path+"." || got.Try != "review the console pack and restore or merge "+tc.path {
				t.Fatalf("%+v", got)
			}
		})
	}
	t.Run("relay disabled", func(t *testing.T) {
		c := fixture(t, "none")
		write(t, c.Root, "console/compose.query.yml", "services:\n  console-query: {image: app}\n")
		if got := localCheck(context.Background(), c); got.Status != doctor.Fail {
			t.Fatal(got)
		}
	})
	t.Run("public route", func(t *testing.T) {
		c := fixture(t, "lite")
		write(t, c.Root, "deploy/Caddyfile.d/leak.caddy", "handle /console/* { reverse_proxy console:7410 }\n")
		if got := localCheck(context.Background(), c); got.Status != doctor.Fail {
			t.Fatal(got)
		}
	})
	t.Run("foreign bridge member", func(t *testing.T) {
		c := fixture(t, "lite")
		write(t, c.Root, "deploy/compose.extra.yml", "services:\n  api:\n    networks: [leak]\nnetworks:\n  leak: {name: todo-console, external: true}\n")
		if got := localCheck(context.Background(), c); got.Status != doctor.Fail {
			t.Fatal(got)
		}
	})
	t.Run("aliased main imports", func(t *testing.T) {
		c := fixture(t, "lite")
		b, err := os.ReadFile(filepath.Join(c.Root, mainPath))
		if err != nil {
			t.Fatal(err)
		}
		source := strings.Replace(string(b), `"os"`, `system "os"`, 1)
		source = strings.Replace(source, `"github.com/stopyellingatme/spinup/kit/x/console"`, `operator "github.com/stopyellingatme/spinup/kit/x/console"`, 1)
		source = strings.ReplaceAll(strings.ReplaceAll(source, "os.", "system."), "console.Main", "operator.Main")
		write(t, c.Root, mainPath, source)
		if got := localCheck(context.Background(), c); got.Status != doctor.OK {
			t.Fatal(got)
		}
	})
	t.Run("disabled", func(t *testing.T) {
		c := fixture(t, "absent")
		write(t, c.Root, "spinup.toml", "modules = []\n[app]\nname = 'todo'\nmodule = 'example.com/todo'\n")
		if got := localCheck(context.Background(), c); got.Status != doctor.Skip || got.Message != "Console module is disabled." {
			t.Fatal(got)
		}
	})
	for _, scope := range []doctor.Scope{doctor.Local, doctor.Remote} {
		found := false
		for _, check := range doctor.Checks(scope, false) {
			if check.ID == "console-wired" || check.ID == "console-ready" {
				found = true
				if check.Scope == doctor.Local && !check.Quick {
					t.Fatal("local check is not quick")
				}
			}
		}
		if !found {
			t.Fatal("check not registered")
		}
	}
}

func overviewJSON(t *testing.T) map[string]any {
	t.Helper()
	at := time.Now().UTC().Format(time.RFC3339)
	doc := map[string]any{"version": 1, "app": "todo", "env": "production", "profile": "lite", "observed_at": at}
	for _, name := range []string{"release", "containers", "alerts", "backup", "wal", "jobs", "disk", "memory", "history"} {
		var data any
		switch name {
		case "containers", "alerts", "jobs", "history":
			data = []any{}
		case "wal":
			data = map[string]any{"state": "waiting", "archived_at": nil, "failed_at": nil, "min_segment": nil, "max_segment": nil}
		case "disk", "memory":
			data = map[string]any{"total_bytes": 100, "used_bytes": 20, "available_bytes": 80}
		}
		doc[name] = map[string]any{"state": "ok", "reason": nil, "at": at, "data": data}
	}
	return doc
}

func TestConsoleReady(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		edit                   func(map[string]any)
		health, inspect, peers string
		exit                   int
		want                   doctor.Status
		section                string
	}{
		{name: "healthy", want: doctor.OK},
		{name: "disabled tile", edit: func(d map[string]any) {
			d["jobs"] = map[string]any{"state": "disabled", "reason": "no_jobs", "at": nil, "data": nil}
		}, want: doctor.OK},
		{name: "wrong identity", edit: func(d map[string]any) { d["app"] = "other" }, want: doctor.Fail},
		{name: "wrong environment", edit: func(d map[string]any) { d["env"] = "staging" }, want: doctor.Fail},
		{name: "wrong profile", edit: func(d map[string]any) { d["profile"] = "full" }, want: doctor.Fail},
		{name: "wrong version", edit: func(d map[string]any) { d["version"] = 2 }, want: doctor.Fail},
		{name: "missing section", edit: func(d map[string]any) { delete(d, "release") }, want: doctor.Fail},
		{name: "invalid record", edit: func(d map[string]any) {
			d["containers"].(map[string]any)["data"] = []any{map[string]any{"name": "fake"}}
		}, want: doctor.Fail},
		{name: "invalid WAL state", edit: func(d map[string]any) { d["wal"].(map[string]any)["data"].(map[string]any)["state"] = "fine" }, want: doctor.Fail},
		{name: "disabled release", edit: func(d map[string]any) {
			d["release"] = map[string]any{"state": "disabled", "reason": "no_jobs", "at": nil, "data": nil}
		}, want: doctor.Fail},
		{name: "unknown state", edit: func(d map[string]any) { d["disk"].(map[string]any)["state"] = "fine" }, want: doctor.Fail},
		{name: "stale", edit: func(d map[string]any) {
			s := d["disk"].(map[string]any)
			s["state"] = "stale"
			s["reason"] = "sample_expired"
		}, want: doctor.Warn, section: "disk"},
		{name: "source outages in schema order", edit: func(d map[string]any) {
			for _, key := range []string{"alerts", "release"} {
				d[key] = map[string]any{"state": "unavailable", "reason": "source_down", "at": d["observed_at"], "data": nil}
			}
		}, want: doctor.Warn, section: "release"},
		{name: "bad process health", health: "not ok\n", want: doctor.Fail},
		{name: "public runtime bind", inspect: `{"running":true,"ports":{"7410/tcp":[{"HostIp":"0.0.0.0","HostPort":"7410"}]},"networks":{"todo-console":{}}}`, want: doctor.Fail},
		{name: "extra runtime binding", inspect: `{"running":true,"ports":{"7410/tcp":[{"HostIp":"127.0.0.1","HostPort":"7410"}],"9000/tcp":[{"HostIp":"0.0.0.0","HostPort":"9000"}]},"networks":{"todo-console":{}}}`, want: doctor.Fail},
		{name: "runtime monitoring attachment", inspect: `{"running":true,"ports":{"7410/tcp":[{"HostIp":"127.0.0.1","HostPort":"7410"}]},"networks":{"todo-console":{},"todo-monitoring":{}}}`, want: doctor.Fail},
		{name: "stopped service", inspect: `{"running":false}`, want: doctor.Fail},
		{name: "missing service", exit: 1, want: doctor.Fail},
		{name: "foreign network peer", peers: `{"driver":"bridge","containers":{"a":{"Name":"todo-console-console-1"},"b":{"Name":"todo-console-console-query-1"},"c":{"Name":"todo-blue-api-1"}}}`, want: doctor.Fail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fixture(t, "lite")
			doc := overviewJSON(t)
			if tc.edit != nil {
				tc.edit(doc)
			}
			body, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			inspect := tc.inspect
			if inspect == "" {
				inspect = `{"running":true,"ports":{"7410/tcp":[{"HostIp":"127.0.0.1","HostPort":"7410"}]},"networks":{"todo-console":{}}}`
			}
			peers := tc.peers
			if peers == "" {
				peers = `{"driver":"bridge","containers":{"a":{"Name":"todo-console-console-1"},"b":{"Name":"todo-console-console-query-1"}}}`
			}
			health := tc.health
			if health == "" {
				health = "ok\n"
			}
			f := &remote.Fake{}
			// The fake accepts only bounded reads. A refresh, tunnel, or write fails the test.
			f.Expect("docker", "inspect").Return(inspect, "", tc.exit)
			f.Expect("docker", "inspect").Return(`{"running":true,"ports":{},"networks":{"todo-console":{},"todo-monitoring":{}}}`, "", 0)
			f.Expect("docker", "network", "inspect").Return(peers, "", 0)
			f.Expect("curl").Return(health, "", 0)
			f.Expect("curl").Return(string(body), "", 0)
			cfg, err := project.Load(c.Root)
			if err != nil {
				t.Fatal(err)
			}
			c.Deps = map[string]any{"remote": f, "config": cfg}
			got := remoteCheck(context.Background(), c)
			if got.Status != tc.want {
				t.Fatalf("%+v, want %v", got, tc.want)
			}
			switch tc.want {
			case doctor.OK:
				if got.Message != "Console is ready." {
					t.Fatal(got)
				}
			case doctor.Fail:
				if got.Message != "Console is unreachable, exposed, or incompatible." || got.Try != "spinup deploy --resume --env production" {
					t.Fatal(got)
				}
			case doctor.Warn:
				if got.Message != "Console source unavailable: "+tc.section+"." || got.Try != "spinup status --env production" {
					t.Fatal(got)
				}
			}
			for _, call := range f.Calls() {
				text := strings.Join(call.Argv, " ")
				if strings.Contains(text, "collect.sh") || strings.Contains(text, "ssh") {
					t.Fatalf("unexpected mutation: %s", text)
				}
			}
			if tc.want == doctor.OK || tc.want == doctor.Warn {
				f.AssertDone(t)
			}
		})
	}
	for _, prof := range []string{"absent", "none", "full"} {
		t.Run(prof, func(t *testing.T) {
			c := fixture(t, prof)
			cfg, err := project.Load(c.Root)
			if err != nil {
				t.Fatal(err)
			}
			f := &remote.Fake{}
			c.Deps = map[string]any{"remote": f}
			f.Expect("docker", "inspect").Return(`{"running":true,"ports":{"7410/tcp":[{"HostIp":"127.0.0.1","HostPort":"7410"}]},"networks":{"todo-console":{}}}`, "", 0)
			peers := `{"driver":"bridge","containers":{"a":{"Name":"todo-console-console-1"}}}`
			if prof == "full" {
				f.Expect("docker", "inspect").Return(`{"running":true,"ports":{},"networks":{"todo-console":{},"todo-monitoring":{}}}`, "", 0)
				peers = `{"driver":"bridge","containers":{"a":{"Name":"todo-console-console-1"},"b":{"Name":"todo-console-console-query-1"}}}`
			}
			f.Expect("docker", "network", "inspect").Return(peers, "", 0)
			f.Expect("curl").Return("ok\n", "", 0)
			doc := overviewJSON(t)
			doc["profile"] = prof
			if !active(cfg) {
				reason := "no_monitoring"
				if prof == "none" {
					reason = "profile_none"
				}
				doc["alerts"] = map[string]any{"state": "disabled", "reason": reason, "at": nil, "data": nil}
			}
			body, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			f.Expect("curl").Return(string(body), "", 0)
			if got := remoteCheck(context.Background(), c); got.Status != doctor.OK {
				t.Fatal(got)
			}
			f.AssertDone(t)
		})
	}
	t.Run("disabled", func(t *testing.T) {
		c := fixture(t, "lite")
		write(t, c.Root, "spinup.toml", "modules = []\n[app]\nname = 'todo'\nmodule = 'example.com/todo'\n")
		got := remoteCheck(context.Background(), c)
		if got.Status != doctor.Skip || got.Message != "Console module is disabled." {
			t.Fatal(got)
		}
	})
}

// This history matches TestProjectHistoryLimits's empty and unknown metadata fixture.
func TestValidOverviewProjectHistoryMetadata(t *testing.T) {
	doc := overviewJSON(t)
	history := `[{"id":"20261122T170000Z-0123456789ab","at":"2026-11-22T17:00:00Z","user":"","git_sha":null,"color":null,"result":"recorded"},{"id":"20261121T170000Z-abcdef012345","at":"2026-11-21T17:00:00Z","user":null,"git_sha":null,"color":null,"result":"recorded"}]`
	doc["history"].(map[string]any)["data"] = json.RawMessage(history)
	doc["containers"].(map[string]any)["data"] = []any{map[string]any{"project": "todo", "service": "caddy", "name": "todo-caddy-1", "state": "running", "health": nil}}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := validOverview(body, "todo", "production", "lite"); !ok {
		t.Fatal("projector history rejected")
	}
	for _, change := range []struct{ from, to string }{
		{`"id":"20261122T170000Z-0123456789ab"`, `"id":""`},
		{`"result":"recorded"`, `"result":""`},
		{`"git_sha":null`, `"git_sha":""`},
		{`"color":null`, `"color":""`},
		{`"name":"todo-caddy-1"`, `"name":""`},
		{`"user":""`, `"user":12`},
	} {
		if _, ok := validOverview([]byte(strings.Replace(string(body), change.from, change.to, 1)), "todo", "production", "lite"); ok {
			t.Errorf("accepted %s", change.to)
		}
	}
}
