package maintain

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/deploy"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// Options selects a maintenance operation and its target environment.
type Options struct {
	Dir, Env, Action        string
	JSON, Plan, Reboot, Now bool
	FailOn, FullTable       string
	Out, ErrOut             io.Writer
}

// Deps supplies replacements for external resources during tests.
type Deps struct {
	Remote  remote.Runner
	Runner  runexec.Runner
	Now     func() time.Time
	Sleep   func(context.Context, time.Duration) error
	DialTLS func(context.Context, string, string, *tls.Config) (*tls.Conn, error)
}

type client struct {
	app, window string
	env         project.Env
	vm          remote.Runner
	now         func() time.Time
	sleep       func(context.Context, time.Duration) error
	dialTLS     func(context.Context, string, string, *tls.Config) (*tls.Conn, error)
	out         io.Writer
	json        bool
}

// Run runs a selected maintenance operation.
func Run(ctx context.Context, o Options, d Deps) error {
	if o.Dir == "" {
		o.Dir = "."
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	root, err := project.FindRoot(o.Dir)
	if err != nil {
		return failure("find project", err)
	}
	cfg, err := project.Load(root)
	if err != nil {
		return failure("load project configuration", err)
	}
	if err := cfg.Validate(); err != nil {
		return failure("validate project configuration", err)
	}
	if o.Env == "" {
		return errors.New("select maintenance environment: --env is missing\nTry: pass --env <environment>")
	}
	env, err := cfg.ResolveEnv(o.Env)
	if err != nil {
		return err
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = sleep
	}
	if d.DialTLS == nil {
		d.DialTLS = func(ctx context.Context, network, address string, config *tls.Config) (*tls.Conn, error) {
			dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: config}
			conn, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			return conn.(*tls.Conn), nil
		}
	}
	vm, owned := d.Remote, false
	if vm == nil {
		host, err := remote.HostFromEnv(env)
		if err != nil {
			return err
		}
		if d.Runner == nil {
			d.Runner = runexec.OSRunner{}
		}
		vm, owned = remote.New(host, d.Runner, remote.Options{}), true
	}
	if owned {
		defer vm.Close(context.Background())
	}
	c := client{app: cfg.App.Name, env: env, window: cfg.Maintenance.Window, vm: vm, now: d.Now, sleep: d.Sleep, dialTLS: d.DialTLS, out: o.Out, json: o.JSON}
	switch o.Action {
	case "patch":
		return c.patch(ctx, o)
	case "vuln-scan":
		return c.vulnScan(ctx, o)
	case "prune":
		return c.prune(ctx, o)
	case "vacuum":
		return c.vacuum(ctx, o)
	case "certs":
		return c.certs(ctx)
	case "window":
		return c.windowStatus()
	default:
		return failure("run maintenance", fmt.Errorf("unknown action %q", o.Action))
	}
}

func failure(action string, err error) error {
	return fmt.Errorf("%s failed: %w\nTry: check spinup.toml, SSH access, and Docker on the VM", action, err)
}

func sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c client) remote(ctx context.Context, argv ...string) (runexec.Result, error) {
	r, err := c.vm.Run(ctx, remote.Cmd{Argv: argv})
	if err != nil {
		return r, failure("run remote command", err)
	}
	return r, nil
}

func (c client) write(value any, text string) error {
	if c.json {
		return json.NewEncoder(c.out).Encode(value)
	}
	_, err := fmt.Fprint(c.out, text)
	return err
}

func (c client) requireWindow(now bool) error {
	if now {
		return nil
	}
	ok, err := InWindow(c.window, c.now())
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("maintenance operation refused outside the maintenance window\nTry: wait for the configured window or pass --now")
	}
	return nil
}

func (c client) patch(ctx context.Context, o Options) error {
	if o.Plan {
		r, err := c.remote(ctx, "apt", "list", "--upgradable")
		if err != nil {
			return err
		}
		return c.write(map[string]any{"upgradable": strings.Fields(string(r.Stdout))}, string(r.Stdout))
	}
	if _, err := c.remote(ctx, "sudo", "apt-get", "update"); err != nil {
		return err
	}
	if _, err := c.remote(ctx, "sudo", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "-y", "upgrade"); err != nil {
		return err
	}
	r, err := c.vm.Run(ctx, remote.Cmd{Argv: []string{"test", "-e", "/var/run/reboot-required"}})
	required := err == nil
	if err != nil && r.ExitCode != 1 {
		return failure("check reboot requirement", err)
	}
	if o.Reboot && required {
		if err := c.requireWindow(o.Now); err != nil {
			return err
		}
		if _, err := c.remote(ctx, "sudo", "reboot"); err != nil {
			return err
		}
		if err := c.waitForVM(ctx); err != nil {
			return err
		}
	}
	return c.write(map[string]any{"reboot_required": required, "rebooted": o.Reboot && required}, fmt.Sprintf("Patch complete. Reboot required: %t\n", required))
}

func (c client) waitForVM(ctx context.Context) error {
	wait, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var lastErr error
	for range 120 {
		_, err := c.vm.Run(wait, remote.Cmd{Argv: []string{"docker", "compose", "-p", c.app, "ps"}})
		if err == nil {
			return nil
		}
		lastErr = err
		if err := c.sleep(wait, time.Second); err != nil {
			return failure("wait for VM after reboot", lastErr)
		}
	}
	return failure("wait for VM after reboot", lastErr)
}

type finding struct {
	Image    string `json:"image"`
	Critical int    `json:"critical"`
	High     int    `json:"high"`
}

func (c client) vulnScan(ctx context.Context, o Options) error {
	image, err := stackversions.MustLoad().Image("trivy")
	if err != nil {
		return failure("load Trivy image", err)
	}
	projects := []string{c.app, c.app + "-blue", c.app + "-green", c.app + "-monitoring"}
	findings := []finding{}
	for _, project := range projects {
		r, err := c.remote(ctx, "docker", "ps", "--filter", "label=com.docker.compose.project="+project, "--format", "{{.Image}}")
		if err != nil {
			return err
		}
		for _, target := range strings.Fields(string(r.Stdout)) {
			r, err = c.remote(ctx, "docker", "run", "--rm", "-v", "/var/run/docker.sock:/var/run/docker.sock:ro", "-v", "spinup-trivy-cache:/root/.cache/", image.Ref(), "image", "--quiet", "--format", "json", "--severity", "HIGH,CRITICAL", target)
			if err != nil {
				return err
			}
			var report struct {
				Results []struct {
					Vulnerabilities []struct {
						Severity string `json:"Severity"`
					} `json:"Vulnerabilities"`
				} `json:"Results"`
			}
			if err := json.Unmarshal(r.Stdout, &report); err != nil {
				return failure("decode Trivy report", err)
			}
			f := finding{Image: target}
			for _, result := range report.Results {
				for _, v := range result.Vulnerabilities {
					if v.Severity == "CRITICAL" {
						f.Critical++
					}
					if v.Severity == "HIGH" {
						f.High++
					}
				}
			}
			findings = append(findings, f)
		}
	}
	if c.json {
		if err := json.NewEncoder(c.out).Encode(findings); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(c.out, "IMAGE CRITICAL HIGH")
		for _, f := range findings {
			fmt.Fprintf(c.out, "%s %d %d\n", f.Image, f.Critical, f.High)
		}
	}
	for _, f := range findings {
		if f.Critical > 0 || (o.FailOn != "critical" && f.High > 0) {
			return &ui.Error{What: "Vulnerability scan found HIGH or CRITICAL issues", Why: "the scanned image has security findings", Try: "update the affected image and deploy it", Code: ui.ExitDrift}
		}
	}
	return nil
}

// pruneImagesScript resolves retained references to image IDs, which also protects tag aliases.
// Container image IDs include stopped containers. All Docker reads must succeed before removal.
const pruneImagesScript = `set -eu
app=$1
shift
keep=''
for ref do
 id=$(docker image inspect --format '{{.Id}}' "$ref")
 keep="$keep
$id"
done
containers=$(docker ps -aq)
if [ -n "$containers" ]; then
 ids=$(docker inspect --format '{{.Image}}' $containers)
 keep="$keep
$ids"
fi
images=$(docker image ls --no-trunc --format '{{.Repository}}:{{.Tag}} {{.ID}}')
printf '%s\n' "$images" | while read -r image id; do
 case "$image" in "$app"-*) ;; *) continue;; esac
 [ -n "$id" ]
 if printf '%s\n' "$keep" | grep -Fxq -- "$id"; then continue; fi
 printf '%s\n' "$image"
done`

func (c client) prune(ctx context.Context, o Options) error {
	releases, err := deploy.ReadReleases(ctx, c.vm, c.app)
	if err != nil {
		return err
	}
	argv := []string{"sh", "-ec", pruneImagesScript, "maintenance-prune", c.app}
	for _, release := range releases[:min(len(releases), deploy.KeepReleases)] {
		for _, image := range release.Images {
			ref := image.Ref
			if ref == "" {
				ref = image.Tag
			}
			if ref == "" {
				ref = image.ID
			}
			if ref == "" {
				return failure("retain release image", fmt.Errorf("release %s has no image reference", release.Release))
			}
			argv = append(argv, ref)
		}
	}
	r, err := c.remote(ctx, argv...)
	if err != nil {
		return err
	}
	images := strings.Fields(string(r.Stdout))
	if o.Plan {
		return c.write(map[string]any{"images": images}, strings.Join(images, "\n")+"\n")
	}
	for _, image := range images {
		if _, err := c.remote(ctx, "docker", "image", "rm", image); err != nil {
			return err
		}
	}
	if _, err := c.remote(ctx, "docker", "builder", "prune", "-f"); err != nil {
		return err
	}
	return c.write(map[string]any{"removed": images}, fmt.Sprintf("Removed %d unused images.\n", len(images)))
}

func (c client) vacuum(ctx context.Context, o Options) error {
	if o.FullTable != "" {
		if err := c.requireWindow(o.Now); err != nil {
			return err
		}
	}
	container, err := c.remote(ctx, "docker", "ps", "--filter", "label=com.docker.compose.project="+c.app, "--filter", "label=com.docker.compose.service=postgres", "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	id := strings.TrimSpace(string(container.Stdout))
	if id == "" {
		return failure("find postgres container", errors.New("postgres is not running"))
	}
	credentials, err := c.remote(ctx, "docker", "exec", id, "printenv", "POSTGRES_USER", "POSTGRES_DB")
	if err != nil {
		return err
	}
	values := strings.Split(strings.TrimSuffix(string(credentials.Stdout), "\n"), "\n")
	if len(values) != 2 || values[0] == "" || values[1] == "" {
		return failure("read postgres environment", errors.New("POSTGRES_USER or POSTGRES_DB is missing or invalid"))
	}
	user, database := values[0], values[1]
	query := "VACUUM (ANALYZE)"
	if o.FullTable != "" {
		query = "VACUUM FULL " + quoteIdent(o.FullTable)
	}
	if _, err := c.remote(ctx, "docker", "exec", id, "psql", "-U", user, "-d", database, "-c", query); err != nil {
		return err
	}
	r, err := c.remote(ctx, "docker", "exec", id, "psql", "-U", user, "-d", database, "-At", "-c", "SELECT relname, n_dead_tup FROM pg_stat_user_tables ORDER BY n_dead_tup DESC LIMIT 10")
	if err != nil {
		return err
	}
	return c.write(map[string]any{"bloat": strings.Fields(string(r.Stdout))}, string(r.Stdout))
}

func quoteIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func (c client) certs(ctx context.Context) error {
	u, err := url.Parse(c.env.PublicURL)
	if err != nil || u.Hostname() == "" {
		return failure("read public certificate", errors.New("public_url is invalid"))
	}
	address := u.Host
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(u.Hostname(), "443")
	}
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := c.dialTLS(dialCtx, "tcp", address, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
	if err != nil {
		return failure("read public certificate", err)
	}
	defer conn.Close()
	cert := conn.ConnectionState().PeerCertificates[0]
	days := int(cert.NotAfter.Sub(c.now()).Hours() / 24)
	v := map[string]any{"issuer": cert.Issuer.String(), "not_after": cert.NotAfter.UTC().Format(time.RFC3339), "days_left": days}
	text := fmt.Sprintf("Issuer: %s\nNot after: %s\nDays left: %d\n", cert.Issuer, cert.NotAfter.UTC().Format(time.RFC3339), days)
	if days < 30 {
		text += "Warning: certificate expires in fewer than 30 days.\n"
	}
	if err := c.write(v, text); err != nil {
		return err
	}
	if days < 7 {
		return errors.New("certificate expires in fewer than seven days\nTry: renew the certificate and run maintenance certs again")
	}
	return nil
}

func (c client) windowStatus() error {
	now := c.now().UTC()
	inside, err := InWindow(c.window, now)
	if err != nil {
		return err
	}
	next := now
	if strings.TrimSpace(c.window) != "" {
		w, err := ParseWindow(c.window)
		if err != nil {
			return err
		}
		next = w.Next(now)
	}
	return c.write(map[string]any{"window": c.window, "inside": inside, "next_start": next.Format(time.RFC3339)}, fmt.Sprintf("Window: %s\nInside: %t\nNext start: %s\n", c.window, inside, next.Format(time.RFC3339)))
}
