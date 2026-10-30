package devloop

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/services"
)

const servicesToml = `
[[services]]
name = "ocr"
port = 8080
dev_port = 8101
health = "/healthz"
openapi = "openapi.yaml"

[[services]]
name = "tts"
port = 9000
dev_port = 8102
health = "/healthz"
`

// serviceProject returns an API-only app. services adds two [[services]] tables, and
// jobs turns on the jobs module.
func serviceProject(t *testing.T, withServices, jobs bool) string {
	t.Helper()
	root := t.TempDir()
	toml := "behavior_version = 3\n"
	if jobs {
		toml += "modules = [\"jobs\"]\n"
	}
	toml += "[app]\nname = \"todo\"\nmodule = \"example.com/todo\"\n"
	if withServices {
		toml += servicesToml
		writeInput(t, root, "services/ocr/openapi.yaml", "openapi: 3.0.0\n")
	}
	writeInput(t, root, "spinup.toml", toml)
	writeInput(t, root, ".env", "PORT=8080\n")
	lock := manifest.New()
	lock.Packs = []string{"base", "api"}
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	return root
}

func findCommand(commands []exec.Cmd, match func(exec.Cmd) bool) (exec.Cmd, bool) {
	for _, c := range commands {
		if match(c) {
			return c, true
		}
	}
	return exec.Cmd{}, false
}

func isUp(c exec.Cmd) bool       { return c.Name == "docker" && slices.Contains(c.Args, "up") }
func isAPI(c exec.Cmd) bool      { return c.Name == "go" && slices.Contains(c.Args, "air") }
func isMigrate(c exec.Cmd) bool  { return c.Name == "go" && slices.Contains(c.Args, "migrate") }
func isWorker(c exec.Cmd) bool   { return c.Name == "go" && slices.Contains(c.Args, "./cmd/worker") }
func isCodegen(c exec.Cmd) bool  { return c.Name == "go" && slices.Contains(c.Args, "oapi-codegen") }
func noPorts(p int) (int, error) { return p, nil }

func TestDevServices(t *testing.T) {
	t.Parallel()
	urls := []string{"SERVICE_OCR_URL=http://127.0.0.1:8101", "SERVICE_TTS_URL=http://127.0.0.1:8102"}
	for _, tc := range []struct {
		name                   string
		services               bool
		noServices, noInfra    bool
		wantFile, wantURLs     bool
		wantUp                 []string
		wantUpWithoutDirectory []string
	}{
		{name: "no services", wantUp: []string{"compose", "-f", "deploy/compose.dev.yml"}, wantUpWithoutDirectory: []string{"up", "-d", "--wait"}},
		{name: "services", services: true, wantFile: true, wantURLs: true,
			wantUp: []string{"compose", "-f", "deploy/compose.dev.yml", "-f", ".spinup/compose.services.yml"}, wantUpWithoutDirectory: []string{"up", "--build", "-d", "--wait"}},
		{name: "no-services", services: true, noServices: true, wantURLs: true,
			wantUp: []string{"compose", "-f", "deploy/compose.dev.yml"}, wantUpWithoutDirectory: []string{"up", "-d", "--wait"}},
		{name: "no-infra", services: true, noInfra: true, wantURLs: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := serviceProject(t, tc.services, false)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fake := &stackFake{cancel: cancel, stopAt: 1}
			var out bytes.Buffer
			o := Options{Dir: root, NoServices: tc.noServices, NoInfra: tc.noInfra, Out: &out, Starter: fake, NoColor: true, PortAllocator: noPorts}
			if err := Run(ctx, o, fake); err != nil {
				t.Fatalf("Run: %v\n%s", err, &out)
			}
			up, found := findCommand(fake.commands, isUp)
			if tc.wantUp == nil {
				if found {
					t.Fatalf("--no-infra ran %v", up.Args)
				}
			} else {
				if !found {
					t.Fatalf("no compose up in %v", fake.events)
				}
				want := slices.Concat(tc.wantUp, []string{"--project-directory", root, "--env-file", filepath.Join(root, ".env")}, tc.wantUpWithoutDirectory)
				if !reflect.DeepEqual(up.Args, want) {
					t.Fatalf("compose up argv:\n got %q\nwant %q", up.Args, want)
				}
			}
			data, err := os.ReadFile(filepath.Join(root, ".spinup/compose.services.yml"))
			if tc.wantFile {
				if err != nil {
					t.Fatal(err)
				}
				for _, want := range []string{"context: services/ocr", "context: services/tts", `"127.0.0.1:8101:8080"`} {
					if !strings.Contains(string(data), want) {
						t.Fatalf("compose.services.yml misses %q:\n%s", want, data)
					}
				}
				if strings.Contains(string(data), "../") {
					t.Fatalf("compose.services.yml has a path outside the root:\n%s", data)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("compose.services.yml written without services: %v", err)
			}
			for _, match := range []func(exec.Cmd) bool{isAPI, isMigrate} {
				c, ok := findCommand(fake.commands, match)
				if !ok {
					t.Fatalf("missing command in %v", fake.events)
				}
				for _, u := range urls {
					if slices.Contains(c.Env, u) != tc.wantURLs {
						t.Fatalf("%v: env %v, want URLs %v", c.Args, c.Env, tc.wantURLs)
					}
				}
			}
			if _, ok := findCommand(fake.commands, isWorker); ok {
				t.Fatal("worker started without the jobs module")
			}
		})
	}
}

func TestDevWorker(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		jobs, noWorker bool
		want           bool
	}{
		{name: "jobs", jobs: true, want: true},
		{name: "no jobs"},
		{name: "no-worker", jobs: true, noWorker: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := serviceProject(t, true, tc.jobs)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			children := 1
			if tc.want {
				children = 2
			}
			fake := &stackFake{cancel: cancel, stopAt: children}
			var out bytes.Buffer
			if err := Run(ctx, Options{Dir: root, NoWorker: tc.noWorker, Out: &out, Starter: fake, NoColor: true, PortAllocator: noPorts}, fake); err != nil {
				t.Fatalf("Run: %v\n%s", err, &out)
			}
			worker, found := findCommand(fake.commands, isWorker)
			if found != tc.want {
				t.Fatalf("worker started = %v, want %v: %v", found, tc.want, fake.events)
			}
			if !found {
				return
			}
			api, _ := findCommand(fake.commands, isAPI)
			if !reflect.DeepEqual(worker.Args, []string{"run", "./cmd/worker"}) || worker.Dir != filepath.Join(root, "api") {
				t.Fatalf("worker command: %+v", worker)
			}
			if !reflect.DeepEqual(worker.Env, api.Env) || !slices.Contains(worker.Env, "SERVICE_OCR_URL=http://127.0.0.1:8101") {
				t.Fatalf("worker env %v, API env %v", worker.Env, api.Env)
			}
			for _, child := range fake.children {
				if !child.terminated {
					t.Fatal("child did not receive TERM")
				}
			}
		})
	}
}

// reloadStarter starts children that run until terminated and counts the starts.
type reloadStarter struct {
	mu      sync.Mutex
	starts  int
	started chan int
}

func (s *reloadStarter) Start(_ context.Context, c exec.Cmd) (Child, error) {
	s.mu.Lock()
	s.starts++
	n := s.starts
	s.mu.Unlock()
	_, _ = c.Stdout.Write([]byte("started\n"))
	s.started <- n
	return &stackChild{done: make(chan struct{})}, nil
}

func TestSupervisorReload(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	starter := &reloadStarter{started: make(chan int, 4)}
	reload := make(chan struct{}, 1)
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- (Supervisor{Starter: starter, Out: &lockedWriter{out: &out}, NoColor: true, Backoff: time.Hour}).Run(ctx, []Process{{Name: "worker", Restart: true, Reload: reload}})
	}()
	waitStart := func(want int) {
		t.Helper()
		select {
		case n := <-starter.started:
			if n != want {
				t.Fatalf("start %d, want %d", n, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("start %d missing", want)
		}
	}
	waitStart(1)
	reload <- struct{}{}
	// The backoff is one hour, so only a reload without backoff starts the child again.
	waitStart(2)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "worker | restarting after a Go change") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestGoWatcher(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeInput(t, root, "api/cmd/api/main.go", "package main")
	reload := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go (goWatcher{Root: root, Reload: reload, Interval: 10 * time.Millisecond, Debounce: 30 * time.Millisecond}).Run(ctx)
	// Changes in skipped paths never reload. Write them first and wait out the debounce.
	time.Sleep(50 * time.Millisecond)
	for _, path := range []string{"api/tmp/api.go", "api/internal/gen/api.gen.go", "api/internal/store/db.go", "api/cmd/api/main_test.go", "api/README.md"} {
		writeInput(t, root, path, "x")
	}
	select {
	case <-reload:
		t.Fatal("reload after a change in a skipped path")
	case <-time.After(200 * time.Millisecond):
	}
	// The first snapshot races with the goroutine start, so repeat the change until a
	// reload arrives.
	deadline := time.After(10 * time.Second)
	for size := 1; ; size++ {
		writeInput(t, root, "api/internal/todo/todo.go", strings.Repeat("x", size))
		select {
		case <-reload:
			return
		case <-time.After(200 * time.Millisecond):
		case <-deadline:
			t.Fatal("no reload after a Go change")
		}
	}
}

func TestWatcherServiceOpenAPI(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeInput(t, root, "services/ocr/openapi.yaml", "openapi: 3.0.0")
	writeInput(t, root, "services/ocr/main.py", "x")
	extra := serviceOpenAPI([]services.Service{{Name: "ocr", OpenAPI: "openapi.yaml"}, {Name: "tts"}})
	if !reflect.DeepEqual(extra, []string{"services/ocr/openapi.yaml"}) {
		t.Fatalf("service OpenAPI files: %v", extra)
	}
	before, err := snapshot(root, extra)
	if err != nil {
		t.Fatal(err)
	}
	writeInput(t, root, "services/ocr/openapi.yaml", "openapi: 3.0.0\npaths: {}")
	writeInput(t, root, "services/ocr/main.py", "changed")
	after, err := snapshot(root, extra)
	if err != nil {
		t.Fatal(err)
	}
	if got := changes(before, after, extra); !reflect.DeepEqual(got, []gen.PostStep{gen.PostServiceClients}) {
		t.Fatalf("steps: %v", got)
	}
}

// TestDevRegeneratesServiceClient runs the whole dev loop. A change to the service
// OpenAPI file must run oapi-codegen through the registered service-clients post-step.
func TestDevRegeneratesServiceClient(t *testing.T) {
	t.Parallel()
	root := serviceProject(t, true, false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fake := &stackFake{cancel: cancel}
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Dir: root, NoInfra: true, Out: &out, Starter: fake, NoColor: true, PortAllocator: noPorts, watchInterval: 10 * time.Millisecond, watchDebounce: 20 * time.Millisecond}, fake)
	}()
	commands := func() []exec.Cmd {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return slices.Clone(fake.commands)
	}
	deadline := time.After(20 * time.Second)
	for size := 1; ; size++ {
		if c, ok := findCommand(commands(), isCodegen); ok {
			if c.Dir != filepath.Join(root, "api") || !slices.Contains(c.Args, "../services/ocr/openapi.yaml") {
				t.Fatalf("oapi-codegen command: %+v", c)
			}
			break
		}
		if _, ok := findCommand(commands(), isAPI); ok {
			writeInput(t, root, "services/ocr/openapi.yaml", "openapi: 3.0.0\n"+strings.Repeat("#", size))
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatalf("no client regeneration: %v", fake.events)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
