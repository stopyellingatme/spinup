package quick

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func TestToolChecks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, command string
		args          []string
		output        string
		err           bool
		check         func(context.Context, *doctor.Context) doctor.Result
		want          doctor.Status
	}{
		{"go supported", "go", []string{"version"}, "go version go1.26.0 darwin/arm64", false, goVersion, doctor.OK},
		{"go newer", "go", []string{"version"}, "go version go2.0.0 linux/amd64", false, goVersion, doctor.OK},
		{"go old", "go", []string{"version"}, "go version go1.25.2 linux/amd64", false, goVersion, doctor.Fail},
		{"go malformed", "go", []string{"version"}, "go version unknown", false, goVersion, doctor.Fail},
		{"go absent", "go", []string{"version"}, "", true, goVersion, doctor.Fail},
		{"node supported", "node", []string{"--version"}, "v24.0.0", false, nodeVersion, doctor.OK},
		{"node old", "node", []string{"--version"}, "v22.17.0", false, nodeVersion, doctor.Fail},
		{"node absent", "node", []string{"--version"}, "", true, nodeVersion, doctor.Fail},
		{"pnpm supported", "pnpm", []string{"--version"}, "12.11.2", false, pnpm, doctor.OK},
		{"pnpm absent", "pnpm", []string{"--version"}, "", true, pnpm, doctor.Fail},
		{"docker running", "docker", []string{"info"}, "Server: Docker Engine", false, docker, doctor.OK},
		{"docker unavailable", "docker", []string{"info"}, "", true, docker, doctor.Fail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &exec.Fake{}
			expect := fake.Expect(tc.command, tc.args...).Return(tc.output, "", 0)
			if tc.err {
				expect.Fail(errors.New("unavailable"))
			}
			result := tc.check(context.Background(), &doctor.Context{Deps: map[string]any{"runner": fake, "web": true}})
			if result.Status != tc.want || tc.want == doctor.Fail && result.Try == "" {
				t.Fatalf("result: %+v", result)
			}
			fake.AssertDone(t)
		})
	}
}
func TestSkippedChecks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		check func(context.Context, *doctor.Context) doctor.Result
		deps  map[string]any
	}{
		{"node without web", nodeVersion, nil}, {"pnpm without web", pnpm, nil}, {"docker no infra", docker, map[string]any{"no-infra": true}}, {"env outside project", envFile, nil}, {"ports outside project", ports, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.check(context.Background(), &doctor.Context{Deps: tc.deps}); got.Status != doctor.Skip {
				t.Fatalf("result %+v", got)
			}
		})
	}
}
func TestMissingRunner(t *testing.T) {
	t.Parallel()
	for _, check := range []func(context.Context, *doctor.Context) doctor.Result{goVersion, nodeVersion, pnpm, docker, ports, envFile} {
		result := check(context.Background(), &doctor.Context{Root: t.TempDir(), Deps: map[string]any{"web": true}})
		if result.Status != doctor.Fail || !strings.Contains(result.Message, "exec.Runner") || result.Try == "" {
			t.Fatalf("result %+v", result)
		}
	}
}
func TestEnvironment(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	c := &doctor.Context{Root: root, Deps: map[string]any{"runner": &exec.Fake{}}}
	if got := envFile(context.Background(), c); got.Status != doctor.Fail || got.Try != "cp .env.example .env" {
		t.Fatal(got)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("PORT=8080"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := envFile(context.Background(), c); got.Status != doctor.OK {
		t.Fatal(got)
	}
}
func TestWebPackDetection(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	lock := manifest.New()
	lock.Packs = []string{"base", "api", "web"}
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	if !web(&doctor.Context{Root: root}) {
		t.Fatal("web pack not detected")
	}
	if web(&doctor.Context{Root: root, Deps: map[string]any{"web": false}}) {
		t.Fatal("no-web override ignored")
	}
}
func TestPortCheck(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, state     string
		relocate, owned bool
		want            doctor.Status
		port            int
	}{
		{name: "free", want: doctor.OK, port: 8080},
		{name: "foreign", want: doctor.Fail, port: 8080},
		{name: "relocate", relocate: true, want: doctor.Warn, port: 8080},
		{name: "owned", owned: true, want: doctor.OK, port: 8080},
		{name: "recorded", state: `{"api":{"port":18080},"web":{"port":15173}}`, owned: true, want: doctor.OK, port: 18080},
		{name: "invalid state", state: `{`, want: doctor.Fail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			fake := &exec.Fake{}
			if tc.state != "" {
				if err := os.Mkdir(filepath.Join(root, ".spinup"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, ".spinup/dev.json"), []byte(tc.state), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.port > 0 && tc.name != "free" && !tc.relocate {
				e := fake.Expect("lsof", "-nP", "-a").Return("p1234\n", "", 0)
				if tc.owned {
					fake.Expect("lsof", "-a", "-p", "1234").Return("p1234\nn"+filepath.Join(root, "api")+"\n", "", 0)
				} else {
					e.Fail(errors.New("foreign process"))
				}
			}
			listen := func(string, string) (net.Listener, error) {
				if tc.name == "free" {
					return &checkListener{}, nil
				}
				return nil, errors.New("busy")
			}
			c := &doctor.Context{Root: root, Deps: map[string]any{"runner": fake, "web": false, "relocate-ports": tc.relocate, "listen": listen}}
			result := ports(context.Background(), c)
			if result.Status != tc.want {
				t.Fatalf("result: %+v", result)
			}
			fake.AssertDone(t)
		})
	}
}

type checkListener struct{}

func (*checkListener) Accept() (net.Conn, error) { return nil, errors.New("not supported") }
func (*checkListener) Close() error              { return nil }
func (*checkListener) Addr() net.Addr            { return &net.TCPAddr{Port: 8080} }

func TestRegisteredQuickChecks(t *testing.T) {
	t.Parallel()
	found := map[string]bool{}
	for _, check := range doctor.Checks(doctor.Local, true) {
		found[check.ID] = check.Quick
	}
	for _, name := range []string{"go-version", "node-version", "pnpm", "docker", "ports", "env-file"} {
		if !found[name] {
			t.Errorf("missing quick check: %s", name)
		}
	}
}
