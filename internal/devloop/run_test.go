package devloop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/stackversions"
)

type stackFake struct {
	mu                      sync.Mutex
	commands                []exec.Cmd
	events                  []string
	cancel                  context.CancelFunc
	stopAt                  int
	failMigrate, failDoctor bool
	children                []*stackChild
}

func (f *stackFake) Run(ctx context.Context, c exec.Cmd) (exec.Result, error) {
	if ctx.Err() != nil {
		return exec.Result{}, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, c)
	args := strings.Join(c.Args, " ")
	f.events = append(f.events, c.Name+" "+args)
	if f.failDoctor && c.Name == "docker" && args == "info" {
		return exec.Result{}, errors.New("Docker stopped")
	}
	if f.failMigrate && args == "run ./cmd/api migrate up" {
		return exec.Result{}, errors.New("bad migration")
	}
	output := ""
	switch c.Name {
	case "go":
		if args == "version" {
			output = "go version go1.27.2 darwin/arm64"
		}
	case "node":
		output = "v24.21.0"
	case "pnpm":
		output = "12.11.2"
	case "git":
		output = "git version 2.50.0"
	}
	return exec.Result{Stdout: []byte(output)}, nil
}
func (f *stackFake) Start(_ context.Context, c exec.Cmd) (Child, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, c)
	f.events = append(f.events, "start "+c.Name+" "+strings.Join(c.Args, " "))
	child := &stackChild{done: make(chan struct{})}
	f.children = append(f.children, child)
	if len(f.children) == f.stopAt {
		f.cancel()
	}
	return child, nil
}

type stackChild struct {
	once       sync.Once
	done       chan struct{}
	terminated bool
}

func (c *stackChild) Wait() error { <-c.done; return nil }
func (c *stackChild) Terminate() error {
	c.terminated = true
	c.once.Do(func() { close(c.done) })
	return nil
}
func (c *stackChild) Kill() error { c.once.Do(func() { close(c.done) }); return nil }
func devProject(t *testing.T, web bool) string {
	t.Helper()
	root := t.TempDir()
	writeInput(t, root, "spinup.toml", "[app]\nname = \"todo\"\nmodule = \"example.com/todo\"\n")
	writeInput(t, root, ".env", "PORT=8080\n")
	lock := manifest.New()
	lock.Packs = []string{"base", "api"}
	if web {
		lock.Packs = append(lock.Packs, "web")
	}
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	return root
}
func (c *stackChild) Alive() bool { return false }

func TestRunOrder(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                                 string
		web, noWeb, noInfra, down, lgtm, wgo bool
		fail                                 string
	}{
		{name: "full", web: true, down: true, lgtm: true},
		{name: "api only"},
		{name: "no web", web: true, noWeb: true},
		{name: "no infra", web: true, noInfra: true},
		{name: "wgo", wgo: true},
		{name: "migration failure", down: true, fail: "migration"},
		{name: "doctor failure", fail: "doctor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := devProject(t, tc.web)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			children := 1
			if tc.web && !tc.noWeb {
				children = 2
			}
			fake := &stackFake{cancel: cancel, stopAt: children, failMigrate: tc.fail == "migration", failDoctor: tc.fail == "doctor"}
			var out bytes.Buffer
			options := Options{Dir: root, NoWeb: tc.noWeb, NoInfra: tc.noInfra, Down: tc.down, LGTM: tc.lgtm, Out: &out, Starter: fake, NoColor: true, PortAllocator: func(port int) (int, error) { return port, nil }}
			if tc.wgo {
				options.Watch = "wgo"
				writeInput(t, root, "api/tools.mod", "module example.com/todo/api\ngo 1.26.0\ntool (\n github.com/bokwoon95/wgo\n)\n")
			}
			err := Run(ctx, options, fake)
			if (err != nil) != (tc.fail != "") {
				t.Fatalf("Run error: %v\n%s", err, &out)
			}
			if tc.fail == "doctor" {
				if len(fake.commands) != 1 || !strings.Contains(err.Error(), "Try: start Docker") {
					t.Fatalf("commands: %v err=%v", fake.events, err)
				}
				return
			}
			migrate, up, api, web, down := -1, -1, -1, -1, -1
			for index, event := range fake.events {
				switch {
				case strings.Contains(event, "migrate up"):
					migrate = index
				case strings.HasSuffix(event, "up -d --wait"):
					up = index
				case strings.HasPrefix(event, "start go"):
					api = index
				case strings.HasPrefix(event, "start pnpm"):
					web = index
				case strings.HasSuffix(event, " down"):
					down = index
				}
			}
			if migrate < 0 || !tc.noInfra && (up < 0 || migrate < up) {
				t.Fatalf("startup order: %v", fake.events)
			}
			if tc.down && (down < migrate || down < api || down < web) {
				t.Fatalf("down order: %v", fake.events)
			}
			if !tc.down && down >= 0 || tc.noInfra && up >= 0 {
				t.Fatalf("unexpected infrastructure call: %v", fake.events)
			}
			if tc.fail == "migration" {
				if api >= 0 || web >= 0 {
					t.Fatal("started processes after migration failed")
				}
				return
			}
			if api < migrate || children == 2 && web < api || children == 1 && web >= 0 {
				t.Fatalf("process order: %v", fake.events)
			}
			for _, child := range fake.children {
				if !child.terminated {
					t.Fatal("child did not receive TERM")
				}
			}
			var ports Ports
			data, err := os.ReadFile(filepath.Join(root, ".spinup/dev.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &ports); err != nil {
				t.Fatal(err)
			}
			if ports.API.Port != 8080 || children == 2 && ports.Web.Port != 5173 {
				t.Fatalf("state: %s", data)
			}
			for _, command := range fake.commands {
				if tc.noInfra && command.Name == "docker" {
					t.Fatal("--no-infra ran Docker")
				}
				if (!tc.web || tc.noWeb) && (command.Name == "node" || command.Name == "pnpm") {
					t.Fatal("web tool ran without web")
				}
				if command.Name == "go" && slices.Contains(command.Args, "tool") {
					if command.Dir != filepath.Join(root, "api") || !slices.Contains(command.Env, "PORT=8080") {
						t.Fatalf("api command: %+v", command)
					}
					if tc.wgo && !slices.Contains(command.Args, "wgo") {
						t.Fatal("wgo not selected")
					}
				}
				if command.Name == "pnpm" && slices.Contains(command.Args, "dev") {
					if !reflect.DeepEqual(command.Env, []string{"API_URL=http://localhost:8080", "PUBLIC_ORIGIN=http://localhost:5173"}) || !slices.Contains(command.Args, "--strictPort") {
						t.Fatalf("web command: %+v", command)
					}
				}
			}
			if tc.lgtm {
				data, err := os.ReadFile(filepath.Join(root, ".spinup/compose.lgtm.yml"))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(data), stackversions.MustLoad().Images["otel-lgtm"].Ref()) {
					t.Fatalf("LGTM override: %s", data)
				}
			}
		})
	}
}

func TestValidationAndWGOGuard(t *testing.T) {
	t.Parallel()
	if err := validate(Options{}, "windows"); err == nil || !strings.Contains(err.Error(), "WSL2") {
		t.Fatalf("Windows: %v", err)
	}
	for _, o := range []Options{{Watch: "bad"}, {Port: -1}, {APIPort: 65536}} {
		if err := validate(o, "linux"); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("invalid options: %+v, %v", o, err)
		}
	}
	for _, tc := range []struct {
		name, text string
		want       bool
	}{
		{"absent", "", false}, {"require only", "require github.com/bokwoon95/wgo v0.6.0", false}, {"comment", "// tool github.com/bokwoon95/wgo", false}, {"single", "tool github.com/bokwoon95/wgo", true}, {"block", "tool (\n github.com/bokwoon95/wgo\n)", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeInput(t, root, "api/tools.mod", tc.text)
			if err := requireWGO(root); (err == nil) != tc.want {
				t.Fatalf("wgo: %v", err)
			}
		})
	}
}
