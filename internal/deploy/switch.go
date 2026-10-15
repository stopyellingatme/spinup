package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// The two colors of a blue/green deploy (ADR 0003).
const (
	Blue  = "blue"
	Green = "green"
)

// DefaultHealthTimeout is the default time that the new color has to become healthy.
const DefaultHealthTimeout = 120 * time.Second

// The drain limits of ADR 0003 step 5. Caddy's grace_period must agree with drainMax.
const (
	drainMin      = time.Second
	drainMax      = 10 * time.Second
	drainInterval = 500 * time.Millisecond
	readyInterval = 2 * time.Second
	stopTimeout   = "15"
)

// UpstreamFile renders upstream.caddy for the live color. Each route of the Caddyfile
// imports it with two arguments: the service and its port (docs/deploy.md).
func UpstreamFile(color string) []byte {
	return []byte(`reverse_proxy {
	dynamic a {
		name {args[0]}-` + color + `
		port {args[1]}
		refresh 1s
	}
	lb_policy round_robin
	lb_try_duration 5s
	lb_try_interval 50ms
	transport http {
		dial_timeout 250ms
	}
}
`)
}

// PlaceholderUpstream is the upstream.caddy file before the first switch. No color is live,
// so Caddy answers 503 until the first release passes its health checks.
var PlaceholderUpstream = []byte("respond \"No release is live yet.\" 503\n")

var upstreamColor = regexp.MustCompile(`(?m)^\s*name \{args\[0\]\}-(blue|green)\s*$`)

// LiveColor returns the color that an upstream.caddy file routes to.
// It returns an empty string when the file names no color.
func LiveColor(upstream []byte) string {
	m := upstreamColor.FindSubmatch(upstream)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// OtherColor returns the color that is not color. An empty color gives Blue.
func OtherColor(color string) string {
	if color == Blue {
		return Green
	}
	return Blue
}

// readUpstreamScript prints the live upstream file when it exists. It never fails for a missing file.
const readUpstreamScript = `if [ -f "$1" ]; then cat -- "$1"; fi`

// ReadUpstream returns the content of /opt/<app>/caddy/upstream.caddy, or nil when it is absent.
func ReadUpstream(ctx context.Context, r remote.Runner, app string) ([]byte, error) {
	root, err := appRoot(app)
	if err != nil {
		return nil, err
	}
	result, err := r.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", readUpstreamScript, "spinup-upstream", root + "/caddy/upstream.caddy"}})
	if err != nil {
		return nil, fmt.Errorf("read the live Caddy upstream: %w\nTry: check the SSH connection and /opt/%s/caddy", err, app)
	}
	if len(result.Stdout) == 0 {
		return nil, nil
	}
	return result.Stdout, nil
}

// SwitchOptions describes one blue/green switch to the release in ReleaseDir.
type SwitchOptions struct {
	// App is the application name. It selects /opt/<app> and the Compose project names.
	App string
	// ReleaseDir is the release directory on the VM. It holds compose.yml and .env.
	ReleaseDir string
	// Color is the color that the switch makes live.
	Color string
	// Services are the color services with their readiness checks.
	Services []Service
	// HealthTimeout limits the start and the readiness checks of the new color.
	HealthTimeout time.Duration
	// NoAutoRollback leaves an unhealthy new color running for debugging.
	NoAutoRollback bool
	// Redact lists values that never reach output or errors.
	Redact []string
	// Out receives progress lines and the streamed Compose output.
	Out io.Writer
	// Now and Sleep default to the real clock.
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
}

// Service is a color service and the HTTP path that proves it is ready.
type Service struct {
	Name string
	Port int
	Path string
}

// DefaultServices returns the color services of the deploy pack. The web service exists only in a full app.
func DefaultServices(web bool) []Service {
	services := []Service{{Name: "api", Port: 8080, Path: "/readyz"}}
	if web {
		services = append(services, Service{Name: "web", Port: 3000, Path: "/healthz"})
	}
	return services
}

// HealthError reports a new color that did not become healthy. The old color stays live.
type HealthError struct {
	Color string
	// Stopped is true when the switch stopped the unhealthy color.
	Stopped bool
	Err     error
}

// Error describes the failure and the state of both colors.
func (e *HealthError) Error() string {
	state := "spinup stopped the new color, and the old color is still live"
	if !e.Stopped {
		state = "the new color is still running for debugging, and the old color is still live"
	}
	return fmt.Sprintf("the %s color did not become healthy: %v\n%s\nTry: read the logs with spinup server logs, correct the problem, and run spinup deploy again", e.Color, e.Err, state)
}

// Unwrap returns the cause.
func (e *HealthError) Unwrap() error { return e.Err }

// Switch makes Color live with the ADR 0003 sequence. It is safe to repeat.
//  1. Start the new color with up -d --wait.
//  2. Check the readiness of each replica from the Caddy container.
//  3. Write upstream.caddy atomically, and keep a copy in the release directory.
//  4. Reload Caddy.
//  5. Drain the old color (at least 1 s, at most 10 s).
//  6. Remove the old color with down -t 15.
//
// When the new color is not healthy, Switch stops it (unless NoAutoRollback) and leaves
// Caddy unchanged. When the reload fails, Switch restores the previous upstream file.
func Switch(ctx context.Context, r remote.Runner, o SwitchOptions) error {
	root, err := appRoot(o.App)
	if err != nil {
		return err
	}
	if o.Color != Blue && o.Color != Green {
		return fmt.Errorf("switch: invalid color %q\nTry: use blue or green", o.Color)
	}
	if !strings.HasPrefix(o.ReleaseDir, root+"/releases/") {
		return fmt.Errorf("switch: release directory %q is outside %s/releases\nTry: pass a release directory of this app", o.ReleaseDir, root)
	}
	s := &switcher{r: r, o: o, root: root}
	s.defaults()
	return s.run(ctx)
}

type switcher struct {
	r    remote.Runner
	o    SwitchOptions
	root string
}

func (s *switcher) defaults() {
	if s.o.HealthTimeout <= 0 {
		s.o.HealthTimeout = DefaultHealthTimeout
	}
	if s.o.Out == nil {
		s.o.Out = io.Discard
	}
	if s.o.Now == nil {
		s.o.Now = time.Now
	}
	if s.o.Sleep == nil {
		s.o.Sleep = sleepContext
	}
	if len(s.o.Services) == 0 {
		s.o.Services = DefaultServices(false)
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (s *switcher) run(ctx context.Context) error {
	app, color, old := s.o.App, s.o.Color, OtherColor(s.o.Color)
	previous, err := ReadUpstream(ctx, s.r, app)
	if err != nil {
		return err
	}
	live := LiveColor(previous)
	caddy, err := s.container(ctx, app, "caddy")
	if err != nil {
		return err
	}
	if caddy == "" {
		return fmt.Errorf("switch: the Caddy container of project %s is not running\nTry: run spinup deploy again, or start it with docker compose -p %s -f compose.edge.yml up -d on the VM", app, app)
	}

	fmt.Fprintf(s.o.Out, "Starting the %s color\n", color)
	deadline := s.o.Now().Add(s.o.HealthTimeout)
	up := s.compose(app+"-"+color, "up", "-d", "--wait", "--wait-timeout", strconv.Itoa(seconds(s.o.HealthTimeout)), "--remove-orphans")
	if _, err := s.remote(ctx, up, true); err != nil {
		return s.unhealthy(ctx, live, fmt.Errorf("docker compose up failed: %w", err))
	}
	if err := s.waitReady(ctx, caddy, color, deadline); err != nil {
		return s.unhealthy(ctx, live, err)
	}

	// Record the addresses of the old color before the switch, so that the drain can watch them.
	oldAddrs, err := s.addresses(ctx, app+"-"+old)
	if err != nil {
		return err
	}

	fmt.Fprintf(s.o.Out, "Switching Caddy to the %s color\n", color)
	upstream := UpstreamFile(color)
	if err := s.r.Upload(ctx, upstream, s.o.ReleaseDir+"/upstream.caddy", 0644); err != nil {
		return fmt.Errorf("switch: write the release copy of upstream.caddy: %w\nTry: check the SSH connection and run spinup deploy --resume", err)
	}
	if err := s.r.Upload(ctx, upstream, s.root+"/caddy/upstream.caddy", 0644); err != nil {
		return fmt.Errorf("switch: write upstream.caddy: %w\nTry: check the SSH connection and run spinup deploy --resume", err)
	}
	reload := []string{"docker", "exec", caddy, "caddy", "reload", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"}
	if _, err := s.remote(ctx, reload, false); err != nil {
		return s.reloadFailed(ctx, previous, live, err)
	}

	if len(oldAddrs) > 0 && live != color {
		fmt.Fprintf(s.o.Out, "Draining the %s color\n", old)
		if err := s.drain(ctx, caddy, oldAddrs); err != nil {
			return err
		}
	}
	fmt.Fprintf(s.o.Out, "Removing the %s color\n", old)
	if _, err := s.remote(ctx, s.compose(app+"-"+old, "down", "-t", stopTimeout, "--remove-orphans"), true); err != nil {
		return fmt.Errorf("switch: remove the %s color: %w\nTry: run spinup deploy --resume, or run docker compose -p %s-%s down on the VM", old, err, app, old)
	}
	return nil
}

func seconds(d time.Duration) int {
	n := int((d + time.Second - 1) / time.Second)
	if n < 1 {
		n = 1
	}
	return n
}

// compose returns a docker compose argv for a color project of the release.
func (s *switcher) compose(project string, args ...string) []string {
	return ComposeArgv(project, s.o.ReleaseDir, []string{s.o.ReleaseDir + "/compose.yml"}, args...)
}

// ComposeArgv returns a docker compose argv with the release .env file and absolute
// Compose files. The project directory is the directory of the first file.
func ComposeArgv(project, releaseDir string, files []string, args ...string) []string {
	argv := []string{"docker", "compose", "-p", project, "--env-file", releaseDir + "/.env"}
	for _, f := range files {
		argv = append(argv, "-f", f)
	}
	return append(argv, args...)
}

func (s *switcher) remote(ctx context.Context, argv []string, stream bool) (runexec.Result, error) {
	c := remote.Cmd{Argv: argv, Redact: s.o.Redact}
	if stream {
		c.Stdout, c.Stderr = s.o.Out, s.o.Out
	}
	return s.r.Run(ctx, c)
}

// container returns the ID of one running container of a Compose service, or "".
func (s *switcher) container(ctx context.Context, project, service string) (string, error) {
	ids, err := s.containers(ctx, project, service)
	if err != nil || len(ids) == 0 {
		return "", err
	}
	return ids[0], nil
}

// containers lists the running containers of a Compose project and service (all services when service is "").
func (s *switcher) containers(ctx context.Context, project, service string) ([]string, error) {
	return listContainers(ctx, s.r, s.o.Redact, project, service)
}

func listContainers(ctx context.Context, r remote.Runner, redact []string, project, service string) ([]string, error) {
	argv := []string{"docker", "ps", "-q", "--filter", "label=com.docker.compose.project=" + project}
	if service != "" {
		argv = append(argv, "--filter", "label=com.docker.compose.service="+service)
	}
	result, err := r.Run(ctx, remote.Cmd{Argv: argv, Redact: redact})
	if err != nil {
		return nil, fmt.Errorf("list the containers of %s: %w\nTry: check that Docker runs on the VM", project, err)
	}
	return strings.Fields(string(result.Stdout)), nil
}

// addresses returns the IP addresses of the running containers of a project on the app network.
func (s *switcher) addresses(ctx context.Context, project string) ([]string, error) {
	ids, err := s.containers(ctx, project, "")
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	return s.inspectAddresses(ctx, ids)
}

func (s *switcher) inspectAddresses(ctx context.Context, ids []string) ([]string, error) {
	format := `{{with index .NetworkSettings.Networks "` + s.o.App + `-app"}}{{.IPAddress}}{{end}}`
	result, err := s.remote(ctx, append([]string{"docker", "inspect", "--format", format}, ids...), false)
	if err != nil {
		return nil, fmt.Errorf("inspect the container addresses: %w\nTry: check that Docker runs on the VM", err)
	}
	return strings.Fields(string(result.Stdout)), nil
}

// waitReady checks each replica of each service through the Caddy container until all pass.
func (s *switcher) waitReady(ctx context.Context, caddy, color string, deadline time.Time) error {
	attempts := max(1, int((s.o.HealthTimeout+readyInterval-1)/readyInterval))
	var last error
	for attempt := 1; ; attempt++ {
		if last = s.checkReady(ctx, caddy, color); last == nil {
			fmt.Fprintf(s.o.Out, "The %s color is ready\n", color)
			return nil
		}
		if attempt >= attempts || !s.o.Now().Before(deadline) {
			return last
		}
		if err := s.o.Sleep(ctx, readyInterval); err != nil {
			return err
		}
	}
}

func (s *switcher) checkReady(ctx context.Context, caddy, color string) error {
	for _, svc := range s.o.Services {
		ids, err := s.containers(ctx, s.o.App+"-"+color, svc.Name)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return fmt.Errorf("no running %s container in the %s color", svc.Name, color)
		}
		addrs, err := s.inspectAddresses(ctx, ids)
		if err != nil {
			return err
		}
		if len(addrs) != len(ids) {
			return fmt.Errorf("a %s container of the %s color has no address on the %s-app network", svc.Name, color, s.o.App)
		}
		for _, addr := range addrs {
			url := "http://" + hostPort(addr, svc.Port) + svc.Path
			if _, err := s.remote(ctx, []string{"docker", "exec", caddy, "wget", "-q", "-T", "3", "-O", "/dev/null", url}, false); err != nil {
				return fmt.Errorf("%s replica %s is not ready at %s", svc.Name, addr, svc.Path)
			}
		}
	}
	return nil
}

func hostPort(addr string, port int) string {
	if strings.Contains(addr, ":") {
		return "[" + addr + "]:" + strconv.Itoa(port)
	}
	return addr + ":" + strconv.Itoa(port)
}

// unhealthy stops the new color unless it is live or NoAutoRollback is set.
func (s *switcher) unhealthy(ctx context.Context, live string, cause error) error {
	e := &HealthError{Color: s.o.Color, Err: cause}
	if s.o.NoAutoRollback {
		return e
	}
	if live == s.o.Color {
		// Never stop the color that serves traffic.
		return fmt.Errorf("the %s color is live but not healthy: %w\nTry: read the logs with spinup server logs, or run spinup rollback", s.o.Color, cause)
	}
	fmt.Fprintf(s.o.Out, "Stopping the unhealthy %s color\n", s.o.Color)
	cleanup := context.WithoutCancel(ctx)
	if _, err := s.remote(cleanup, s.compose(s.o.App+"-"+s.o.Color, "down", "-t", stopTimeout), true); err != nil {
		return errors.Join(e, fmt.Errorf("stop the unhealthy %s color: %w\nTry: run docker compose -p %s-%s down on the VM", s.o.Color, err, s.o.App, s.o.Color))
	}
	e.Stopped = true
	return e
}

// reloadFailed restores the previous upstream file. Caddy keeps its old configuration after a failed reload.
func (s *switcher) reloadFailed(ctx context.Context, previous []byte, live string, cause error) error {
	cleanup := context.WithoutCancel(ctx)
	var errs []error
	if previous != nil {
		if err := s.r.Upload(cleanup, previous, s.root+"/caddy/upstream.caddy", 0644); err != nil {
			errs = append(errs, fmt.Errorf("restore upstream.caddy: %w", err))
		}
	}
	errs = append([]error{fmt.Errorf("the Caddy reload failed, and Caddy keeps the old configuration: %w\nTry: check the Caddyfile with caddy validate, and run spinup deploy again", cause)}, errs...)
	if !s.o.NoAutoRollback && live != s.o.Color {
		if _, err := s.remote(cleanup, s.compose(s.o.App+"-"+s.o.Color, "down", "-t", stopTimeout), true); err != nil {
			errs = append(errs, fmt.Errorf("stop the %s color: %w", s.o.Color, err))
		}
	}
	return errors.Join(errs...)
}

type caddyUpstream struct {
	Address     string `json:"address"`
	NumRequests int    `json:"num_requests"`
}

// drain waits until Caddy has no request in flight to an old address, at least drainMin and at most drainMax.
func (s *switcher) drain(ctx context.Context, caddy string, old []string) error {
	oldSet := map[string]bool{}
	for _, a := range old {
		oldSet[a] = true
	}
	argv := []string{"docker", "exec", caddy, "wget", "-q", "-T", "2", "-O", "-", "http://localhost:2019/reverse_proxy/upstreams"}
	waited := time.Duration(0)
	for {
		if err := s.o.Sleep(ctx, step(waited)); err != nil {
			return err
		}
		waited += step(waited)
		busy := true
		if result, err := s.remote(ctx, argv, false); err == nil {
			var upstreams []caddyUpstream
			if json.Unmarshal(result.Stdout, &upstreams) == nil {
				busy = false
				for _, u := range upstreams {
					host := u.Address
					if i := strings.LastIndex(host, ":"); i >= 0 {
						host = strings.Trim(host[:i], "[]")
					}
					if oldSet[host] && u.NumRequests > 0 {
						busy = true
					}
				}
			}
		}
		if !busy || waited >= drainMax {
			return nil
		}
	}
}

// step returns the next drain wait: drainMin first, then drainInterval.
func step(waited time.Duration) time.Duration {
	if waited == 0 {
		return drainMin
	}
	return drainInterval
}
