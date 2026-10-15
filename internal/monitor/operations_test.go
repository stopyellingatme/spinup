package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/deploy"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func expectAPI(f *remote.Fake, body string) {
	f.Expect("docker", "ps").Return("abc123", "", 0)
	f.Expect("sh", "-c").Return(body, "", 0)
}

func TestMutatingRequests(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ action, body, want, path string }{
		{"silence", `{"silenceID":"silence-123"}`, "silence-123", "/api/alertmanager/grafana/api/v2/silences"},
		{"test-alert", `{}`, "spinup-test-1000000000", "/api/alertmanager/grafana/api/v2/alerts"},
	} {
		t.Run(tt.action, func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			expectAPI(f, tt.body)
			var out bytes.Buffer
			c := client{vm: f, app: "todo", out: &out, now: func() time.Time { return time.Unix(1, 0) }}
			if err := c.run(context.Background(), Options{Action: tt.action, Matcher: "alertname=Down", For: "2h"}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tt.want) {
				t.Fatal(out.String())
			}
			call := f.Calls()[1]
			if !strings.Contains(strings.Join(call.Argv, " "), tt.path) {
				t.Fatal(call.Argv)
			}
			var payload any
			if err := json.Unmarshal([]byte(call.Argv[len(call.Argv)-1]), &payload); err != nil {
				t.Fatal(err)
			}
			if tt.action == "silence" {
				body := payload.(map[string]any)
				if body["endsAt"] != "1970-01-01T02:00:01Z" {
					t.Fatal(body)
				}
			} else {
				body := payload.([]any)[0].(map[string]any)
				if body["labels"].(map[string]any)["alertname"] != "SpinupTestAlert" {
					t.Fatal(body)
				}
			}
			f.AssertDone(t)
		})
	}
}

func TestInvalidQueries(t *testing.T) {
	t.Parallel()
	for _, o := range []Options{{Action: "trace", TraceID: "bad"}, {Action: "logs", Since: "bad"}, {Action: "silence", Matcher: "x", For: "2h"}, {Action: "silence", Matcher: "a=b", For: "-1h"}} {
		t.Run(o.Action+o.Matcher+o.For, func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			c := client{vm: f, profile: "full"}
			if err := c.run(context.Background(), o); err == nil {
				t.Fatal("invalid input accepted")
			}
			if len(f.Calls()) != 0 {
				t.Fatal("invalid input reached VM")
			}
		})
	}
	c := client{profile: "lite"}
	if err := c.trace(context.Background(), strings.Repeat("a", 32)); err == nil || !strings.Contains(err.Error(), "full") {
		t.Fatal(err)
	}
}

func TestStatusFormats(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`[{"Service":"loki","State":"running","Health":"healthy"}]`, `{"Service":"loki","State":"running","Health":"healthy"}`} {
		t.Run(body[:1], func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			f.Expect("docker", "compose", "-p", "todo-monitoring").Return(body, "", 0)
			f.Expect("docker", "volume", "ls").Return("todo-monitoring_loki\n", "", 0)
			f.Expect("docker", "system", "df").Return("Local Volumes space usage:\nVOLUME NAME LINKS SIZE\ntodo-monitoring_loki 1 42MB\nother_loki 1 1GB\n", "", 0)
			var out bytes.Buffer
			c := client{vm: f, app: "todo", profile: "lite", out: &out}
			if err := c.status(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"Profile: lite", "loki running healthy", "todo-monitoring_loki 42MB"} {
				if !strings.Contains(out.String(), want) {
					t.Fatal(out.String())
				}
			}
			if strings.Contains(out.String(), "other_loki") {
				t.Fatal(out.String())
			}
			f.AssertDone(t)
		})
	}
}

func TestDashboardSync(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := &remote.Fake{}
	expectAPI(f, `[{"uid":"stack"},{"uid":"custom"}]`)
	expectAPI(f, `{"meta":{"provisioned":false},"dashboard":{"uid":"custom","id":17,"title":"Edited","panels":[]}}`)
	expectAPI(f, `{"meta":{"provisioned":true,"provisionedExternalId":"/var/lib/grafana/dashboards/stack/red.json"},"dashboard":{"uid":"stack","id":19}}`)
	var out bytes.Buffer
	c := client{vm: f, app: "todo", root: root, out: &out}
	if err := c.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "monitoring/grafana/dashboards/custom/custom.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\n  \"panels\": [],\n  \"title\": \"Edited\",\n  \"uid\": \"custom\"\n}\n" {
		t.Fatal(string(data))
	}
	if _, err = os.Stat(filepath.Join(root, "monitoring/grafana/dashboards/custom/stack.json")); !os.IsNotExist(err) {
		t.Fatal("exported stack dashboard")
	}
	f.AssertDone(t)
}

func TestDashboardSymlinks(t *testing.T) {
	t.Parallel()
	for _, component := range []string{"monitoring", "monitoring/grafana/dashboards/custom", "monitoring/grafana/dashboards/custom/custom.json"} {
		t.Run(component, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			target := t.TempDir()
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, component)), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, component)); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err = writeDashboard(root, "monitoring/grafana/dashboards/custom/custom.json", []byte("{}")); err == nil {
				t.Fatal("accepted symlink")
			}
		})
	}
}

func TestAnnotation(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, present, id string }{{"no pack", "", ""}, {"stopped", "present", ""}, {"running", "present", "abc123"}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			f.Expect("docker", "ps").Return(tt.id, "", 0)
			release := "20261015T120000Z-abcdef123456"
			if tt.id != "" {
				f.Expect("cat").Return(`{"release":"`+release+`","git_sha":"abcdef1234567890","user":"alice"}`, "", 0)
				expectAPI(f, `{"id":42}`)
			}
			if err := annotate(context.Background(), &deploy.HookContext{App: "todo", Release: release, Remote: f}); err != nil {
				t.Fatal(err)
			}
			if tt.id != "" {
				calls := f.Calls()
				args := calls[len(calls)-1].Argv
				payload := args[len(args)-1]
				for _, want := range []string{release, "abcdef1234567890", "alice"} {
					if !strings.Contains(payload, want) {
						t.Fatal(payload)
					}
				}
			}
			f.AssertDone(t)
		})
	}
	found := false
	for _, h := range deploy.PostReleaseHooks() {
		if h.Name == "grafana-annotation" {
			found = true
		}
	}
	if !found {
		t.Fatal("hook not registered")
	}
}

func TestTunnelArguments(t *testing.T) {
	t.Setenv("SPINUP_SSH_CONFIG", "/tmp/ssh-config")
	f := &runexec.Fake{}
	f.Expect("ssh").Return("", "", 0)
	vm := remote.New(remote.Host{Address: "vm", User: "deploy", Port: 2222}, tunnelRunner{runner: f, port: 12345}, remote.Options{CacheDir: "/tmp/su-monitor-test"})
	if _, err := vm.Run(context.Background(), remote.Cmd{Argv: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	args := f.Calls()[0].Args
	for _, want := range []string{"-N", "ExitOnForwardFailure=yes", "127.0.0.1:12345:127.0.0.1:3000", "ControlPersist=60s", "BatchMode=yes", "ConnectTimeout=10", "2222", "/tmp/ssh-config"} {
		if !strings.Contains(strings.Join(args, " "), want) {
			t.Fatal(args)
		}
	}
	if !reflect.DeepEqual(args[len(args)-2:], []string{"--", "deploy@vm"}) {
		t.Fatal(args)
	}
	f.AssertDone(t)
}

func TestAuthenticatedFailure(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	f.Expect("docker", "ps").Return("abc123", "", 0)
	f.Expect("sh", "-c").Fail(errors.New("password-must-not-appear"))
	c := client{vm: f, app: "todo"}
	_, err := c.api(context.Background(), "grafana", "GET", "/api/search", nil)
	if err == nil || strings.Contains(err.Error(), "password-must-not-appear") {
		t.Fatal(err)
	}
	f.AssertDone(t)
}

func TestRunProject(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "spinup.toml"), []byte("modules=['monitoring']\n[app]\nname='todo'\nmodule='example.com/todo'\n[env.staging]\nhost='vm'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	f := &remote.Fake{}
	f.Expect("docker", "compose").Return("[]", "", 0)
	f.Expect("docker", "volume", "ls").Return("", "", 0)
	f.Expect("docker", "system", "df").Return("", "", 0)
	if err := Run(context.Background(), Options{Dir: dir, Env: "staging", Action: "status"}, Deps{Remote: f}); err != nil {
		t.Fatal(err)
	}
	f.AssertDone(t)
	// The environment still uses project validation before any remote command.
	_, err := remote.HostFromEnv(project.Env{Host: "-bad"})
	if err == nil {
		t.Fatal("invalid host")
	}
}

func TestTunnelCancellationArguments(t *testing.T) {
	t.Parallel()
	f := &runexec.Fake{}
	f.Expect("ssh").Return("", "", 0)
	vm := remote.New(remote.Host{Address: "vm"}, tunnelRunner{runner: f, port: 12345, cancel: true}, remote.Options{CacheDir: "/tmp/su-monitor-cancel"})
	if _, err := vm.Run(context.Background(), remote.Cmd{Argv: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	args := f.Calls()[0].Args
	want := []string{"-O", "cancel", "-L", "127.0.0.1:12345:127.0.0.1:3000", "--", "vm"}
	if !reflect.DeepEqual(args[len(args)-len(want):], want) {
		t.Fatal(args)
	}
	f.AssertDone(t)
}

func TestProvisionedCustomSync(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	f := &remote.Fake{}
	expectAPI(f, `[{"uid":"custom"}]`)
	expectAPI(f, `{"meta":{"provisioned":true,"provisionedExternalId":"/var/lib/grafana/dashboards/custom/custom.json"},"dashboard":{"uid":"custom","id":17,"title":"Edited"}}`)
	var out bytes.Buffer
	c := client{vm: f, app: "todo", root: root, out: &out}
	if err := c.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "monitoring/grafana/dashboards/custom/custom.json"))
	if err != nil || !bytes.Contains(data, []byte("Edited")) {
		t.Fatalf("data=%s err=%v", data, err)
	}
	f.AssertDone(t)
}

func TestDashboardHardLink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	custom := "monitoring/grafana/dashboards/custom/custom.json"
	stack := "monitoring/grafana/dashboards/stack/stack.json"
	for _, path := range []string{custom, stack} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, stack), []byte("managed"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, stack), filepath.Join(dir, custom)); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = writeDashboard(root, custom, []byte("edited")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, stack))
	if err != nil || string(data) != "managed" {
		t.Fatalf("stack=%s err=%v", data, err)
	}
}

func TestSpanTree(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	f.Expect("docker", "ps").Return("abc123", "", 0)
	f.Expect("docker", "exec").Return(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"api"}}]},"scopeSpans":[{"spans":[{"spanId":"child","parentSpanId":"parent","name":"query","startTimeUnixNano":"1000000","endTimeUnixNano":"2000000"},{"spanId":"parent","name":"GET /","startTimeUnixNano":"0","endTimeUnixNano":"3000000"}]}]}]}`, "", 0)
	var out bytes.Buffer
	c := client{vm: f, app: "todo", profile: "full", out: &out}
	if err := c.trace(context.Background(), strings.Repeat("a", 32)); err != nil {
		t.Fatal(err)
	}
	if out.String() != "api GET / 3ms\n  api query 1ms\n" {
		t.Fatal(out.String())
	}
	f.AssertDone(t)
}

func TestMalformedResponses(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"logs", "alerts", "trace", "silence", "dashboards sync"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			f.Expect("docker", "ps").Return("abc123", "", 0)
			if action == "logs" || action == "trace" {
				f.Expect("docker", "exec").Return("not JSON", "", 0)
			} else {
				f.Expect("sh", "-c").Return("not JSON", "", 0)
			}
			var out bytes.Buffer
			c := client{vm: f, app: "todo", profile: "full", root: t.TempDir(), out: &out, now: func() time.Time { return time.Unix(1, 0) }}
			err := c.run(context.Background(), Options{Action: action, TraceID: strings.Repeat("a", 32), Matcher: "alertname=Down", For: "1h"})
			if err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatal(err)
			}
			f.AssertDone(t)
		})
	}
}
