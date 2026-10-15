package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// Options selects a monitoring operation and its environment.
type Options struct {
	Dir, Env, Action                             string
	Service, Since, Query, TraceID, Matcher, For string
	NoBrowser, AcceptNewHostKey                  bool
	Out, ErrOut                                  io.Writer
}

// Deps supplies the process runner and an optional remote connection for tests.
type Deps struct {
	Exec   runexec.Runner
	Remote remote.Runner
	Now    func() time.Time
}

type client struct {
	vm                 remote.Runner
	app, profile, root string
	out                io.Writer
	now                func() time.Time
}

// Run operates a monitoring stack through the environment's SSH connection.
func Run(ctx context.Context, o Options, d Deps) error {
	if o.Dir == "" {
		o.Dir = "."
	}
	root, err := project.FindRoot(o.Dir)
	if err != nil {
		return failure("load monitoring project", err)
	}
	cfg, err := project.Load(root)
	if err != nil {
		return failure("load monitoring configuration", err)
	}
	if err = cfg.Validate(); err != nil {
		return failure("validate monitoring configuration", err)
	}
	if !slices.Contains(cfg.Modules, "monitoring") || cfg.Monitoring.Profile == "none" {
		return failure("operate monitoring", fmt.Errorf("the monitoring pack is disabled"))
	}
	env, err := cfg.ResolveEnv(o.Env)
	if err != nil {
		return err
	}
	host, err := remote.HostFromEnv(env)
	if err != nil {
		return err
	}
	if d.Exec == nil {
		d.Exec = runexec.OSRunner{}
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.ErrOut == nil {
		o.ErrOut = io.Discard
	}
	if o.Action == "open" {
		return open(ctx, o, host, d.Exec)
	}
	vm := d.Remote
	if vm == nil {
		vm = remote.New(host, d.Exec, remote.Options{AcceptNewHostKey: o.AcceptNewHostKey})
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = vm.Close(closeCtx)
		}()
	}
	c := client{vm: vm, app: cfg.App.Name, profile: cfg.Monitoring.Profile, root: root, out: o.Out, now: d.Now}
	return c.run(ctx, o)
}

func failure(action string, err error) error {
	return fmt.Errorf("%s failed: %w\nTry: check spinup.toml, the SSH connection, and the monitoring stack", action, err)
}

func (c *client) run(ctx context.Context, o Options) error {
	switch o.Action {
	case "status":
		return c.status(ctx)
	case "logs":
		return c.logs(ctx, o)
	case "trace":
		return c.trace(ctx, o.TraceID)
	case "alerts":
		data, err := c.api(ctx, "grafana", "GET", "/api/alertmanager/grafana/api/v2/alerts", nil)
		if err != nil {
			return err
		}
		var alerts []struct {
			Labels map[string]string `json:"labels"`
			Status struct {
				State string `json:"state"`
			} `json:"status"`
		}
		if err = json.Unmarshal(data, &alerts); err != nil {
			return failure("decode alerts", err)
		}
		for _, a := range alerts {
			if a.Status.State != "suppressed" {
				state := a.Status.State
				if state == "active" {
					state = "firing"
				}
				fmt.Fprintf(c.out, "%s %s\n", state, a.Labels["alertname"])
			}
		}
		// Grafana-managed rules expose Pending separately from Alertmanager's active alerts.
		return c.pending(ctx)
	case "silence":
		return c.silence(ctx, o)
	case "test-alert":
		return c.testAlert(ctx)
	case "dashboards sync":
		return c.sync(ctx)
	default:
		return failure("operate monitoring", fmt.Errorf("unknown action %q", o.Action))
	}
}

func (c *client) container(ctx context.Context, service string) (string, error) {
	res, err := c.vm.Run(ctx, remote.Cmd{Argv: []string{"docker", "ps", "--filter", "label=com.docker.compose.project=" + c.app + "-monitoring", "--filter", "label=com.docker.compose.service=" + service, "--format", "{{.ID}}"}})
	if err != nil {
		return "", failure("find monitoring container", err)
	}
	id := strings.TrimSpace(string(res.Stdout))
	if id == "" {
		return "", nil
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return "", failure("find monitoring container", fmt.Errorf("invalid container ID"))
		}
	}
	return id, nil
}

// The password crosses only a pipe on the VM. Neither SSH argv nor captured output contains it.
const httpScript = `set -eu
url=$1; method=$2; body=$3; auth=$4
if [ "$auth" = yes ]; then IFS= read -r password; fi
if command -v curl >/dev/null 2>&1; then
 if [ "$auth" = yes ]; then set -- -u "admin:$password"; else set --; fi
 if [ "$method" = POST ]; then set -- "$@" -H 'Content-Type: application/json' --data-binary "$body"; fi
 exec curl -fsS -H 'Accept: application/json' --max-time 30 "$@" "$url"
fi
if command -v wget >/dev/null 2>&1; then
 if [ "$auth" = yes ]; then set -- --header "Authorization: Basic $(printf '%s' "admin:$password" | base64 | tr -d '\n')"; else set --; fi
 if [ "$method" = POST ]; then set -- "$@" --header 'Content-Type: application/json' --post-data="$body"; fi
 exec wget -q -T 30 -O - --header 'Accept: application/json' "$@" "$url"
fi
printf '%s\n' 'The container needs curl or wget.' >&2
exit 1`

func (c *client) api(ctx context.Context, service, method, path string, body any) ([]byte, error) {
	id, err := c.container(ctx, service)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, failure("query "+service, fmt.Errorf("the container is not running"))
	}
	port := map[string]string{"grafana": "3000", "loki": "3100", "tempo": "3200"}[service]
	payload := ""
	if body != nil {
		data, e := json.Marshal(body)
		if e != nil {
			return nil, failure("encode request", e)
		}
		payload = string(data)
	}
	args := []string{"docker", "exec", id, "sh", "-c", httpScript, "spinup-http", "http://127.0.0.1:" + port + path, method, payload, "no"}
	if service == "grafana" {
		// Positional arguments preserve shell quoting for all user input.
		script := `set -eu; { cat "$1"; printf '\n'; } | docker exec -i "$2" sh -c "$3" spinup-http "$4" "$5" "$6" yes`
		args = []string{"sh", "-c", script, "spinup-grafana", "/opt/" + c.app + "/secrets/GRAFANA_ADMIN_PASSWORD", id, httpScript, "http://127.0.0.1:" + port + path, method, payload}
	}
	res, err := c.vm.Run(ctx, remote.Cmd{Argv: args, Timeout: 40 * time.Second})
	if err != nil {
		// Authenticated failures must not expose arbitrary server diagnostics.
		if service == "grafana" {
			return nil, failure("query Grafana", fmt.Errorf("the authenticated request failed"))
		}
		return nil, failure("query "+service, err)
	}
	return res.Stdout, nil
}

func (c *client) status(ctx context.Context) error {
	fmt.Fprintf(c.out, "Profile: %s\n", c.profile)
	res, err := c.vm.Run(ctx, remote.Cmd{Argv: []string{"docker", "compose", "-p", c.app + "-monitoring", "ps", "--format", "json"}})
	if err != nil {
		return failure("read monitoring status", err)
	}
	var rows []struct{ Name, Service, State, Health string }
	// Compose versions return either an array or newline-delimited objects.
	if strings.HasPrefix(strings.TrimSpace(string(res.Stdout)), "[") {
		err = json.Unmarshal(res.Stdout, &rows)
	} else {
		dec := json.NewDecoder(strings.NewReader(string(res.Stdout)))
		for {
			var row struct{ Name, Service, State, Health string }
			e := dec.Decode(&row)
			if e == io.EOF {
				break
			}
			if e != nil {
				err = e
				break
			}
			rows = append(rows, row)
		}
	}
	if err != nil {
		return failure("decode monitoring status", err)
	}
	for _, r := range rows {
		fmt.Fprintf(c.out, "%s %s %s\n", r.Service, r.State, r.Health)
	}
	// Select volumes by Compose labels because explicit volume names can override the project prefix.
	volumeResult, err := c.vm.Run(ctx, remote.Cmd{Argv: []string{"docker", "volume", "ls", "--filter", "label=com.docker.compose.project=" + c.app + "-monitoring", "--format", "{{.Name}}"}})
	if err != nil {
		return failure("find monitoring volumes", err)
	}
	volumes := map[string]bool{}
	for _, name := range strings.Fields(string(volumeResult.Stdout)) {
		volumes[name] = true
	}
	res, err = c.vm.Run(ctx, remote.Cmd{Argv: []string{"docker", "system", "df", "-v"}})
	if err != nil {
		return failure("read monitoring disk use", err)
	}
	for line := range strings.SplitSeq(string(res.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && volumes[fields[0]] {
			fmt.Fprintf(c.out, "%s %s\n", fields[0], strings.Join(fields[2:], " "))
		}
	}
	return nil
}
