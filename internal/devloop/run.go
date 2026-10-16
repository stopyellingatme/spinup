package devloop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stopyellingatme/spinup/internal/doctor"
	_ "github.com/stopyellingatme/spinup/internal/doctor/quick" // Register development prerequisites.
	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/webtool"
)

// Options controls the local development stack.
type Options struct {
	// Dir selects a directory within the application.
	Dir string
	// NoWeb skips Vite and the web prerequisites.
	NoWeb bool
	// NoInfra skips Docker startup and shutdown.
	NoInfra bool
	// LGTM enables the pinned local monitoring container.
	LGTM bool
	// Down stops infrastructure owned by this run on exit.
	Down bool
	// NoColor disables colors in process prefixes.
	NoColor bool
	// Port and APIPort select preferred ports. Zero selects the defaults.
	Port, APIPort int
	// Watch selects air or wgo. The default is air.
	Watch string
	// Out receives prerequisite results, URLs, and child output.
	Out io.Writer
	// Starter overrides OSStarter for embedded callers and lifecycle tests.
	Starter Starter
	// PortAllocator overrides local TCP probing for embedded callers.
	PortAllocator func(int) (int, error)
}

// Endpoint records one local development port.
type Endpoint struct {
	// Port is the service port on localhost.
	Port int `json:"port"`
}

// Ports records the local service endpoints in .spinup/dev.json.
type Ports struct {
	// API is the Go HTTP endpoint.
	API Endpoint `json:"api"`
	// Web is the Vite endpoint, or zero when disabled.
	Web Endpoint `json:"web"`
	// Mailpit is the mail inspection endpoint.
	Mailpit Endpoint `json:"mailpit"`
	// Grafana is the monitoring endpoint, or zero when disabled.
	Grafana Endpoint `json:"grafana"`
}

// Run checks prerequisites and runs infrastructure, migrations, and dev processes.
func Run(ctx context.Context, o Options, runner exec.Runner) (runErr error) {
	if err := validate(o, runtime.GOOS); err != nil {
		return err
	}
	if runner == nil {
		runner = exec.OSRunner{}
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	// Commands, watchers, and child log lines share the same output lock.
	o.Out = &lockedWriter{out: o.Out}
	if o.Dir == "" {
		o.Dir = "."
	}
	root, err := project.FindRoot(o.Dir)
	if err != nil {
		return err
	}
	behavior := 1
	if cfg, err := project.Load(root); err == nil {
		behavior = cfg.EffectiveBehaviorVersion()
	}
	lock, err := manifest.Load(filepath.Join(root, project.LockFile))
	if err != nil {
		return err
	}
	hasWeb := slices.Contains(lock.Packs, "web") && !o.NoWeb
	if o.Watch == "" {
		o.Watch = "air"
	}
	if o.Watch == "wgo" {
		if err := requireWGO(root); err != nil {
			return err
		}
	}
	if err := prerequisites(ctx, root, o, hasWeb, runner); err != nil {
		return err
	}
	running := map[string]int{}
	if !o.NoInfra {
		base := []string{"compose", "-f", "deploy/compose.dev.yml", "--project-directory", root, "--env-file", filepath.Join(root, ".env")}
		running = publishedPorts(ctx, runner, base, root)
	}
	ports, err := allocate(o, hasWeb, running)
	if err != nil {
		return err
	}
	if err := writeState(root, ports); err != nil {
		return err
	}
	compose, err := composeArgs(root, o, ports)
	if err != nil {
		return err
	}
	if !o.NoInfra {
		if o.Down {
			defer func() {
				downCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				defer cancel()
				if err := runCommand(downCtx, runner, exec.Cmd{Name: "docker", Args: append(slices.Clone(compose), "down"), Dir: root, Stdout: o.Out, Stderr: o.Out}); err != nil {
					runErr = errors.Join(runErr, err)
				}
			}()
		}
		if err := runCommand(ctx, runner, exec.Cmd{Name: "docker", Args: append(slices.Clone(compose), "up", "-d", "--wait"), Dir: root, Stdout: o.Out, Stderr: o.Out}); err != nil {
			return err
		}
	}
	// The API trusts PUBLIC_ORIGIN for form-action calls from SvelteKit (kit auth), so it must
	// match the web dev server, even when the web port moved.
	apiEnv := []string{"SPINUP_ENV=dev", "SPINUP_BEHAVIOR_VERSION=" + strconv.Itoa(behavior), "PORT=" + strconv.Itoa(ports.API.Port), "PUBLIC_ORIGIN=http://localhost:" + strconv.Itoa(ports.Web.Port)}
	if o.LGTM {
		apiEnv = append(apiEnv, "OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318")
	}
	if err := runCommand(ctx, runner, exec.Cmd{Name: "go", Args: []string{"run", "./cmd/api", "migrate", "up"}, Dir: filepath.Join(root, "api"), Env: apiEnv, Stdout: o.Out, Stderr: o.Out}); err != nil {
		return err
	}
	apiArgs := []string{"tool", "-modfile=tools.mod", "air", "-c", ".air.toml"}
	if o.Watch == "wgo" {
		apiArgs = []string{"tool", "-modfile=tools.mod", "wgo", "run", "./cmd/api"}
	}
	processes := []Process{{Name: "api", Dir: filepath.Join(root, "api"), Cmd: exec.Cmd{Name: "go", Args: apiArgs, Env: apiEnv}, Restart: true}}
	if hasWeb {
		tool := webtool.FromLock(lock)
		cmd := tool.InWeb(root, tool.Run("dev", "--port", strconv.Itoa(ports.Web.Port), "--strictPort"))
		cmd.Env = []string{"API_URL=http://localhost:" + strconv.Itoa(ports.API.Port), "PUBLIC_ORIGIN=http://localhost:" + strconv.Itoa(ports.Web.Port)}
		processes = append(processes, Process{Name: "web", Dir: cmd.Dir, Cmd: cmd, Restart: true})
	}
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	watchDone := make(chan error, 1)
	go func() {
		err := (Watcher{Root: root, Runner: runner, Out: o.Out}).Run(watchCtx)
		watchDone <- err
		if err != nil {
			cancel()
		}
	}()
	err = (Supervisor{Starter: o.Starter, Out: o.Out, NoColor: o.NoColor, OnReady: func() { printURLs(o.Out, ports, hasWeb, o.LGTM) }}).Run(watchCtx, processes)
	cancel()
	watchErr := <-watchDone
	return errors.Join(err, watchErr)
}

func validate(o Options, platform string) error {
	if platform == "windows" {
		return fmt.Errorf("spinup dev requires Unix process groups\nTry: use WSL2 on Windows")
	}
	if o.Watch != "" && o.Watch != "air" && o.Watch != "wgo" {
		return fmt.Errorf("unknown dev watcher %q\nTry: use --watch air or --watch wgo", o.Watch)
	}
	for _, port := range []int{o.Port, o.APIPort} {
		if port < 0 || port > 65535 {
			return fmt.Errorf("invalid development port %d\nTry: choose a port from 1 through 65535", port)
		}
	}
	return nil
}
func requireWGO(root string) error {
	path := filepath.Join(root, "api/tools.mod")
	data, err := os.ReadFile(path)
	if err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		block := false
		for scanner.Scan() {
			line, _, _ := strings.Cut(scanner.Text(), "//")
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if fields[0] == "tool" && len(fields) > 1 {
				if fields[1] == "(" {
					block = true
					continue
				}
				if strings.HasSuffix(strings.Trim(fields[1], "\""), "/wgo") {
					return nil
				}
			} else if block {
				if fields[0] == ")" {
					block = false
					continue
				}
				if strings.HasSuffix(strings.Trim(fields[0], "\""), "/wgo") {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("cannot use wgo: api/tools.mod does not declare a wgo tool\nTry: use --watch air or declare wgo in api/tools.mod")
}
func prerequisites(ctx context.Context, root string, o Options, web bool, runner exec.Runner) error {
	c := &doctor.Context{Root: root, Quick: true, Deps: map[string]any{"runner": runner, "web": web, "no-infra": o.NoInfra, "relocate-ports": true, "api-port": o.APIPort, "web-port": o.Port}}
	for _, check := range doctor.Checks(doctor.Local, true) {
		result := check.Run(ctx, c)
		_, _ = fmt.Fprintf(o.Out, "%s\t%s\t%s\n", result.Status, check.ID, result.Message)
		if result.Status == doctor.Fail {
			return fmt.Errorf("dev prerequisite %s failed: %s\nTry: %s", check.ID, result.Message, result.Try)
		}
	}
	return nil
}

// allocate picks the local ports. running holds the host ports that the app's own running
// Compose services already publish (see publishedPorts); those are reused without a probe.
func allocate(o Options, web bool, running map[string]int) (Ports, error) {
	api, front := o.APIPort, o.Port
	if api == 0 {
		api = 8080
	}
	if front == 0 {
		front = 5173
	}
	ports := Ports{API: Endpoint{api}}
	if !o.NoInfra {
		ports.Mailpit.Port = 8025
	}
	if web {
		ports.Web.Port = front
	}
	if o.LGTM {
		ports.Grafana.Port = 3000
	}
	endpoints := []*Endpoint{&ports.API}
	if web {
		endpoints = append(endpoints, &ports.Web)
	}
	if !o.NoInfra {
		endpoints = append(endpoints, &ports.Mailpit)
	}
	if o.LGTM && !o.NoInfra {
		endpoints = append(endpoints, &ports.Grafana)
	}
	free := o.PortAllocator
	if free == nil {
		free = FreePort
	}
	used := map[int]bool{}
	held := map[*Endpoint]int{&ports.Mailpit: running["mailpit:8025"], &ports.Grafana: running["otel-lgtm:3000"]}
	for _, endpoint := range endpoints {
		if port := held[endpoint]; port > 0 && !used[port] {
			endpoint.Port = port
			used[port] = true
			continue
		}
		preferred := endpoint.Port
		if used[preferred] {
			preferred = 0
		}
		for {
			port, err := free(preferred)
			if err != nil {
				return Ports{}, err
			}
			if !used[port] {
				endpoint.Port = port
				used[port] = true
				break
			}
			preferred = 0
		}
	}
	return ports, nil
}
func writeState(root string, ports Ports) error {
	data, err := json.MarshalIndent(ports, "", "  ")
	if err != nil {
		return err
	}
	return writeDevFile(root, "dev.json", append(data, '\n'))
}
func writeDevFile(root, name string, data []byte) error {
	directory := filepath.Join(root, ".spinup")
	if err := os.MkdirAll(directory, 0755); err != nil {
		return fmt.Errorf("create dev directory: %w\nTry: check project permissions", err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
		return fmt.Errorf("write dev %s: %w\nTry: check project permissions", name, err)
	}
	return nil
}
func composeArgs(root string, o Options, ports Ports) ([]string, error) {
	args := []string{"compose", "-f", "deploy/compose.dev.yml"}
	if o.NoInfra {
		return args, nil
	}
	if ports.Mailpit.Port != 8025 {
		// Compose normally appends ports. !override replaces the managed list.
		data := fmt.Sprintf("services:\n  mailpit:\n    ports: !override\n      - \"127.0.0.1:%d:8025\"\n      - \"127.0.0.1:1025:1025\"\n", ports.Mailpit.Port)
		if err := writeDevFile(root, "compose.ports.yml", []byte(data)); err != nil {
			return nil, err
		}
		args = append(args, "-f", ".spinup/compose.ports.yml")
	}
	if o.LGTM {
		stack, err := stackversions.Load()
		if err != nil {
			return nil, fmt.Errorf("load LGTM image: %w\nTry: run spinup doctor", err)
		}
		image, err := stack.Image("otel-lgtm")
		if err != nil {
			return nil, fmt.Errorf("load LGTM image: %w\nTry: run spinup doctor", err)
		}
		data := fmt.Sprintf("services:\n  otel-lgtm:\n    image: %s\n    ports:\n      - \"127.0.0.1:%d:3000\"\n      - \"127.0.0.1:4317:4317\"\n      - \"127.0.0.1:4318:4318\"\n", image.Ref(), ports.Grafana.Port)
		if err := writeDevFile(root, "compose.lgtm.yml", []byte(data)); err != nil {
			return nil, err
		}
		args = append(args, "-f", ".spinup/compose.lgtm.yml")
	}
	return append(args, "--project-directory", root, "--env-file", filepath.Join(root, ".env")), nil
}
func runCommand(ctx context.Context, r exec.Runner, c exec.Cmd) error {
	result, err := r.Run(ctx, c)
	if err != nil {
		return fmt.Errorf("dev command failed: %w\nTry: inspect the command output and repeat spinup dev", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("dev command %s exited with code %d\nTry: inspect the command output and repeat spinup dev", c.String(), result.ExitCode)
	}
	return nil
}
func printURLs(out io.Writer, ports Ports, web, lgtm bool) {
	if web {
		_, _ = fmt.Fprintf(out, "App      http://localhost:%d\n", ports.Web.Port)
	}
	_, _ = fmt.Fprintf(out, "API      http://localhost:%d\n", ports.API.Port)
	if ports.Mailpit.Port != 0 {
		_, _ = fmt.Fprintf(out, "Mailpit  http://localhost:%d\n", ports.Mailpit.Port)
	}
	if lgtm {
		_, _ = fmt.Fprintf(out, "Grafana  http://localhost:%d\n", ports.Grafana.Port)
	}
}

type lockedWriter struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *lockedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.Write(data)
}
