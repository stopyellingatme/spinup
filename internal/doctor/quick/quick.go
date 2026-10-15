// Package quick registers local checks required by the development loop.
package quick

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func init() {
	for _, check := range []doctor.Check{
		{ID: "go-version", Title: "Go version", Run: goVersion},
		{ID: "node-version", Title: "Node version", Run: nodeVersion},
		{ID: "pnpm", Title: "pnpm executable", Run: pnpm},
		{ID: "docker", Title: "Docker daemon", Run: docker},
		{ID: "ports", Title: "Development ports", Run: ports},
		{ID: "env-file", Title: "Development environment", Run: envFile},
	} {
		check.Scope = doctor.Local
		check.Quick = true
		doctor.Register(check)
	}
}

func runner(c *doctor.Context) (exec.Runner, doctor.Result) {
	r, ok := c.Deps["runner"].(exec.Runner)
	if !ok {
		return nil, fail("quick check requires an exec.Runner", "run spinup doctor with a configured runner")
	}
	return r, doctor.Result{}
}
func fail(message, hint string) doctor.Result {
	return doctor.Result{Status: doctor.Fail, Message: message, Try: hint}
}
func skip() doctor.Result {
	return doctor.Result{Status: doctor.Skip, Message: "check does not apply to this project"}
}
func enabled(c *doctor.Context, key string) bool { value, _ := c.Deps[key].(bool); return value }
func web(c *doctor.Context) bool {
	if value, ok := c.Deps["web"].(bool); ok {
		return value
	}
	if c.Root == "" {
		return false
	}
	lock, err := manifest.Load(filepath.Join(c.Root, "spinup.lock"))
	if err == nil {
		for _, pack := range lock.Packs {
			if pack == "web" {
				return true
			}
		}
		return false
	}
	_, err = os.Stat(filepath.Join(c.Root, "web/package.json"))
	return err == nil
}
func command(ctx context.Context, c *doctor.Context, name string, args ...string) (string, error) {
	r, _ := runner(c)
	result, err := r.Run(ctx, exec.Cmd{Name: name, Args: args, Dir: c.Root, Timeout: 10 * time.Second})
	if err == nil && result.ExitCode != 0 {
		err = fmt.Errorf("%s exited with code %d", name, result.ExitCode)
	}
	return strings.TrimSpace(string(result.Stdout)), err
}
func version(output, prefix string, minimumMajor, minimumMinor int) bool {
	for _, field := range strings.Fields(output) {
		if !strings.HasPrefix(field, prefix) {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(field, prefix), ".")
		if len(parts) < 2 {
			continue
		}
		major, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		minor, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		return major > minimumMajor || major == minimumMajor && minor >= minimumMinor
	}
	return false
}
func goVersion(ctx context.Context, c *doctor.Context) doctor.Result {
	if _, result := runner(c); result.Status == doctor.Fail {
		return result
	}
	output, err := command(ctx, c, "go", "version")
	if err != nil || !version(output, "go", 1, 26) {
		return fail("Go 1.26 or later is required", "install Go 1.26 or later and repeat spinup doctor")
	}
	return doctor.Result{Status: doctor.OK, Message: output}
}
func nodeVersion(ctx context.Context, c *doctor.Context) doctor.Result {
	if !web(c) {
		return skip()
	}
	if _, result := runner(c); result.Status == doctor.Fail {
		return result
	}
	output, err := command(ctx, c, "node", "--version")
	if err != nil || !version(output, "v", 24, 0) {
		return fail("Node 24 or later is required", "install Node 24 or later and repeat spinup doctor")
	}
	return doctor.Result{Status: doctor.OK, Message: output}
}
func pnpm(ctx context.Context, c *doctor.Context) doctor.Result {
	if !web(c) {
		return skip()
	}
	if _, result := runner(c); result.Status == doctor.Fail {
		return result
	}
	output, err := command(ctx, c, "pnpm", "--version")
	if err != nil {
		return fail("pnpm is not available on PATH", "enable pnpm through corepack and add it to PATH")
	}
	return doctor.Result{Status: doctor.OK, Message: "pnpm " + output}
}
func docker(ctx context.Context, c *doctor.Context) doctor.Result {
	if enabled(c, "no-infra") {
		return skip()
	}
	if _, result := runner(c); result.Status == doctor.Fail {
		return result
	}
	_, err := command(ctx, c, "docker", "info")
	if err != nil {
		return fail("Docker daemon did not answer docker info", "start Docker and repeat spinup doctor")
	}
	return doctor.Result{Status: doctor.OK, Message: "Docker daemon is available"}
}
func envFile(_ context.Context, c *doctor.Context) doctor.Result {
	if c.Root == "" {
		return skip()
	}
	if _, result := runner(c); result.Status == doctor.Fail {
		return result
	}
	info, err := os.Stat(filepath.Join(c.Root, ".env"))
	if err != nil || !info.Mode().IsRegular() {
		return fail("the project .env file is missing or unreadable", "cp .env.example .env")
	}
	return doctor.Result{Status: doctor.OK, Message: ".env exists"}
}
func ports(ctx context.Context, c *doctor.Context) doctor.Result {
	if c.Root == "" {
		return skip()
	}
	if _, result := runner(c); result.Status == doctor.Fail {
		return result
	}
	wanted := struct {
		API, Web struct {
			Port int `json:"port"`
		}
	}{}
	wanted.API.Port, wanted.Web.Port = 8080, 5173
	data, err := os.ReadFile(filepath.Join(c.Root, ".spinup/dev.json"))
	if err == nil {
		if err := json.Unmarshal(data, &wanted); err != nil {
			return fail("cannot decode .spinup/dev.json", "remove .spinup/dev.json and repeat spinup dev")
		}
	} else if !os.IsNotExist(err) {
		return fail("cannot read .spinup/dev.json", "check the file permissions")
	}
	if value, ok := c.Deps["api-port"].(int); ok && value > 0 {
		wanted.API.Port = value
	}
	if value, ok := c.Deps["web-port"].(int); ok && value > 0 {
		wanted.Web.Port = value
	}
	numbers := []int{wanted.API.Port}
	if web(c) {
		numbers = append(numbers, wanted.Web.Port)
	}
	listen, _ := c.Deps["listen"].(func(string, string) (net.Listener, error))
	if listen == nil {
		var config net.ListenConfig
		listen = func(network, address string) (net.Listener, error) { return config.Listen(ctx, network, address) }
	}
	for _, port := range numbers {
		if port < 1 || port > 65535 {
			return fail("invalid development port", "remove .spinup/dev.json and choose a valid port")
		}
		listener, err := listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			_ = listener.Close()
			continue
		}
		if enabled(c, "relocate-ports") {
			return doctor.Result{Status: doctor.Warn, Message: fmt.Sprintf("port %d is busy. Dev will select free ports", port)}
		}
		if ownsPort(ctx, c, port) {
			continue
		}
		return fail(fmt.Sprintf("port %d is busy outside this app", port), "stop the other app or run spinup dev to select free ports")
	}
	return doctor.Result{Status: doctor.OK, Message: "development ports are available to this app"}
}

func ownsPort(ctx context.Context, c *doctor.Context, port int) bool {
	// Never infer ownership from dev.json alone. Verify each listener's working directory.
	output, err := command(ctx, c, "lsof", "-nP", "-a", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-Fp")
	if err != nil {
		return false
	}
	found := false
	for _, field := range strings.Fields(output) {
		pid, ok := strings.CutPrefix(field, "p")
		if !ok {
			continue
		}
		if _, err := strconv.Atoi(pid); err != nil {
			return false
		}
		cwd, err := command(ctx, c, "lsof", "-a", "-p", pid, "-d", "cwd", "-Fn")
		if err != nil {
			return false
		}
		owned := false
		for _, line := range strings.Split(cwd, "\n") {
			path, ok := strings.CutPrefix(line, "n")
			if !ok {
				continue
			}
			relative, err := filepath.Rel(c.Root, path)
			if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				owned = true
			}
		}
		if !owned {
			return false
		}
		found = true
	}
	return found
}
