// Package remote holds doctor checks that run on a deployed VM.
package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/backup"
	"github.com/stopyellingatme/spinup/internal/doctor"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/modules"
	"github.com/stopyellingatme/spinup/internal/project"
	sshremote "github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// Options selects the deployed application that Doctor checks.
type Options struct {
	// Dir is the project directory. Empty selects the current directory.
	Dir string
	// Env names the deployment environment.
	Env string
	// JSON writes a JSON report to Out.
	JSON bool
	// Out receives the report.
	Out io.Writer
	// Runner replaces SSH for tests. Nil uses the operating system runner.
	Runner runexec.Runner
	// Remote replaces the SSH connection for tests.
	Remote sshremote.Runner
}

func init() {
	doctor.Register(doctor.Check{ID: "remote-backup-age", Title: "Backup age", Scope: doctor.Remote, Run: backupAge})
	doctor.Register(doctor.Check{ID: "remote-certs", Title: "TLS certificate", Scope: doctor.Remote, Run: certs})
	doctor.Register(doctor.Check{ID: "remote-clock", Title: "Clock synchronization", Scope: doctor.Remote, Run: clock})
	doctor.Register(doctor.Check{ID: "remote-deploy-lock", Title: "Deploy lock", Scope: doctor.Remote, Run: deployLock})
	doctor.Register(doctor.Check{ID: "remote-disk", Title: "Disk space", Scope: doctor.Remote, Run: disk})
	doctor.Register(doctor.Check{ID: "remote-docker", Title: "Docker", Scope: doctor.Remote, Run: docker})
	doctor.Register(doctor.Check{ID: "remote-monitoring", Title: "Monitoring", Scope: doctor.Remote, Run: monitoring})
	doctor.Register(doctor.Check{ID: "remote-secrets-dir", Title: "Secrets directory", Scope: doctor.Remote, Run: secretsDir})
	doctor.Register(doctor.Check{ID: "remote-unattended-upgrades", Title: "Unattended upgrades", Scope: doctor.Remote, Run: unattendedUpgrades})
}

// Doctor runs every registered remote check and reports the results.
func Doctor(ctx context.Context, o Options) error {
	dir := o.Dir
	if dir == "" {
		dir = "."
	}
	root, err := project.FindRoot(dir)
	if err != nil {
		return fmt.Errorf("locate project for remote doctor: %w", err)
	}
	config, err := project.Load(root)
	if err != nil {
		return fmt.Errorf("load project for remote doctor: %w\nTry: correct spinup.toml", err)
	}
	if o.Env == "" {
		return errors.New("select remote doctor environment: --env is missing\nTry: pass --env <environment>")
	}
	env, err := config.ResolveEnv(o.Env)
	if err != nil {
		return fmt.Errorf("select remote doctor environment: %w", err)
	}
	r := o.Remote
	owned := false
	if r == nil {
		host, err := sshremote.HostFromEnv(env)
		if err != nil {
			return err
		}
		runner := o.Runner
		if runner == nil {
			runner = runexec.OSRunner{}
		}
		r, owned = sshremote.New(host, runner, sshremote.Options{}), true
	}
	if owned {
		defer r.Close(context.Background())
	}
	reports := doctor.RunAll(ctx, doctor.Remote, &doctor.Context{Root: root, Env: env.Name, Deps: map[string]any{
		"remote": r, "config": config, "environment": env,
	}})
	return printReports(reports, o)
}

func printReports(reports []doctor.Report, o Options) error {
	type report struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Message string `json:"message"`
		Try     string `json:"try,omitempty"`
	}
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	result := make([]report, 0, len(reports))
	failed := false
	for _, item := range reports {
		result = append(result, report{item.Check.ID, item.Result.Status.String(), item.Result.Message, item.Result.Try})
		failed = failed || item.Result.Status == doctor.Fail
		if !o.JSON {
			fmt.Fprintf(out, "%s\t%s\t%s\n", item.Result.Status, item.Check.ID, item.Result.Message)
			if item.Result.Try != "" {
				fmt.Fprintf(out, "Try: %s\n", item.Result.Try)
			}
		}
	}
	if o.JSON {
		if err := json.NewEncoder(out).Encode(result); err != nil {
			return err
		}
	}
	if failed {
		return &ui.Error{What: "Doctor checks failed", Why: "a remote health check failed", Try: "correct the reported failures and run spinup doctor --remote --env <environment>", Code: ui.ExitError}
	}
	return nil
}

func remoteRunner(c *doctor.Context) (sshremote.Runner, *project.Config, project.Env, doctor.Result, bool) {
	if c == nil {
		return nil, nil, project.Env{}, doctor.Result{Status: doctor.Skip, Message: "No remote connection was supplied."}, false
	}
	r, ok := c.Deps["remote"].(sshremote.Runner)
	if !ok || r == nil {
		return nil, nil, project.Env{}, doctor.Result{Status: doctor.Skip, Message: "No remote connection was supplied."}, false
	}
	config, _ := c.Deps["config"].(*project.Config)
	env, _ := c.Deps["environment"].(project.Env)
	if config == nil {
		return nil, nil, project.Env{}, doctor.Result{Status: doctor.Skip, Message: "No project configuration was supplied."}, false
	}
	return r, config, env, doctor.Result{}, true
}

func run(ctx context.Context, r sshremote.Runner, argv ...string) (string, error) {
	result, err := r.Run(ctx, sshremote.Cmd{Argv: argv})
	return strings.TrimSpace(string(result.Stdout)), err
}

func appPath(config *project.Config, suffix string) string { return "/opt/" + config.App.Name + suffix }

func disk(ctx context.Context, c *doctor.Context) doctor.Result {
	r, _, _, skip, ok := remoteRunner(c)
	if !ok {
		return skip
	}
	worst := doctor.Result{Status: doctor.OK, Message: "Disk use is below 80%."}
	for _, path := range []string{"/", "/var/lib/docker"} {
		result := diskPath(ctx, r, path)
		if result.Status > worst.Status {
			worst = result
		}
	}
	return worst
}

func diskPath(ctx context.Context, r sshremote.Runner, path string) doctor.Result {
	out, err := run(ctx, r, "df", "-P", path)
	if err != nil {
		return doctor.Result{Status: doctor.Fail, Message: "Cannot inspect disk space.", Try: "Check SSH access and the df command."}
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		return doctor.Result{Status: doctor.Fail, Message: "Cannot read disk use.", Try: "Check df output on the VM."}
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 5 {
		return doctor.Result{Status: doctor.Fail, Message: "Cannot read disk use.", Try: "Check df output on the VM."}
	}
	used, err := strconv.Atoi(strings.TrimSuffix(fields[4], "%"))
	if err != nil {
		return doctor.Result{Status: doctor.Fail, Message: "Cannot read disk use.", Try: "Check df output on the VM."}
	}
	status := doctor.OK
	if used > 90 {
		status = doctor.Fail
	} else if used > 80 {
		status = doctor.Warn
	}
	return doctor.Result{Status: status, Message: fmt.Sprintf("Disk use on %s is %d%%.", path, used), Try: "Free disk space before deployment."}
}

func docker(ctx context.Context, c *doctor.Context) doctor.Result {
	r, config, _, skip, ok := remoteRunner(c)
	if !ok {
		return skip
	}
	if _, err := run(ctx, r, "docker", "info"); err != nil {
		return doctor.Result{Status: doctor.Fail, Message: "Docker is not available.", Try: "Start Docker and check deploy user access."}
	}
	out, err := run(ctx, r, "docker", "compose", "--project-name", config.App.Name, "ps", "--status", "running", "--services")
	if err != nil || out == "" {
		return doctor.Result{Status: doctor.Fail, Message: "The application stack is not running.", Try: "Run spinup deploy status --env " + c.Env + "."}
	}
	return doctor.Result{Status: doctor.OK, Message: "Docker and the application stack are running."}
}

func deployLock(ctx context.Context, c *doctor.Context) doctor.Result {
	r, config, _, skip, ok := remoteRunner(c)
	if !ok {
		return skip
	}
	out, err := run(ctx, r, "cat", appPath(config, "/deploy.lock/owner.json"))
	if err != nil {
		return doctor.Result{Status: doctor.OK, Message: "No deploy lock is held."}
	}
	var lock struct {
		Acquired   time.Time `json:"acquired"`
		TTLSeconds int64     `json:"ttl_seconds"`
	}
	if json.Unmarshal([]byte(out), &lock) != nil || lock.TTLSeconds <= 0 {
		return doctor.Result{Status: doctor.Warn, Message: "The deploy lock metadata is invalid.", Try: "Inspect the deploy lock before removal."}
	}
	if time.Now().After(lock.Acquired.Add(time.Duration(lock.TTLSeconds) * time.Second)) {
		return doctor.Result{Status: doctor.Warn, Message: "An expired deploy lock is held.", Try: "Inspect it, then run spinup deploy unlock --force --env " + c.Env + "."}
	}
	return doctor.Result{Status: doctor.OK, Message: "An active deploy lock is held."}
}

func unattendedUpgrades(ctx context.Context, c *doctor.Context) doctor.Result {
	r, _, _, skip, ok := remoteRunner(c)
	if !ok {
		return skip
	}
	out, err := run(ctx, r, "systemctl", "is-enabled", "unattended-upgrades")
	if err != nil || out != "enabled" {
		return doctor.Result{Status: doctor.Warn, Message: "Unattended upgrades are not enabled.", Try: "Enable the unattended-upgrades service."}
	}
	return doctor.Result{Status: doctor.OK, Message: "Unattended upgrades are enabled."}
}

func clock(ctx context.Context, c *doctor.Context) doctor.Result {
	r, _, _, skip, ok := remoteRunner(c)
	if !ok {
		return skip
	}
	out, err := run(ctx, r, "timedatectl", "show", "-p", "NTPSynchronized", "--value")
	if err != nil {
		return doctor.Result{Status: doctor.Skip, Message: "timedatectl is not available."}
	}
	if out != "yes" && out != "true" {
		return doctor.Result{Status: doctor.Warn, Message: "The VM clock is not synchronized.", Try: "Enable NTP synchronization."}
	}
	return doctor.Result{Status: doctor.OK, Message: "The VM clock is synchronized."}
}

func secretsDir(ctx context.Context, c *doctor.Context) doctor.Result {
	r, config, env, skip, ok := remoteRunner(c)
	if !ok {
		return skip
	}
	out, err := run(ctx, r, "stat", "-c", "%a:%U", appPath(config, "/secrets"))
	if err != nil {
		return doctor.Result{Status: doctor.Fail, Message: "Cannot inspect the secrets directory.", Try: "Create the secrets directory with deploy user ownership."}
	}
	parts := strings.Split(out, ":")
	if len(parts) != 2 || !safeDirMode(parts[0]) || (env.User != "" && parts[1] != env.User) {
		return doctor.Result{Status: doctor.Fail, Message: "The secrets directory has unsafe permissions or owner.", Try: "Set mode 0700 and deploy user ownership."}
	}
	files, err := run(ctx, r, "find", appPath(config, "/secrets"), "-type", "f", "-printf", "%m:%u\\n")
	if err != nil {
		return doctor.Result{Status: doctor.Fail, Message: "Cannot inspect secret files.", Try: "Check secrets directory access."}
	}
	for line := range strings.SplitSeq(files, "\n") {
		if line == "" {
			continue
		}
		p := strings.Split(line, ":")
		if len(p) != 2 || !safeFileMode(p[0]) || (env.User != "" && p[1] != env.User) {
			return doctor.Result{Status: doctor.Fail, Message: "A secret file has unsafe permissions or owner.", Try: "Set secret files to mode 0600 and deploy user ownership."}
		}
	}
	return doctor.Result{Status: doctor.OK, Message: "The secrets directory has safe permissions."}
}

func safeDirMode(mode string) bool {
	n, err := strconv.ParseUint(mode, 8, 32)
	return err == nil && n&0077 == 0
}
func safeFileMode(mode string) bool {
	n, err := strconv.ParseUint(mode, 8, 32)
	return err == nil && n == 0600
}

func backupAge(ctx context.Context, c *doctor.Context) doctor.Result {
	r, config, _, skip, ok := remoteRunner(c)
	if !ok {
		return skip
	}
	if !modules.Enabled(config, "backups") {
		return doctor.Result{Status: doctor.OK, Message: "The backups module is not enabled."}
	}
	out, err := run(ctx, r, "cat", backup.MetricsPath(config.App.Name))
	if err != nil {
		return doctor.Result{Status: doctor.Warn, Message: "Cannot read backup metrics.", Try: "Run spinup backup now --env " + c.Env + "."}
	}
	var newest float64
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.HasPrefix(fields[0], "backup_last_success_timestamp_seconds") {
			value, err := strconv.ParseFloat(fields[1], 64)
			if err == nil && value > newest {
				newest = value
			}
		}
	}
	if newest == 0 {
		return doctor.Result{Status: doctor.Warn, Message: "No successful backup metric exists.", Try: "Run spinup backup now --env " + c.Env + "."}
	}
	age := time.Since(time.Unix(int64(newest), 0))
	if age > 48*time.Hour {
		return doctor.Result{Status: doctor.Fail, Message: "The newest backup is older than 48 hours.", Try: "Run spinup backup now --env " + c.Env + "."}
	}
	if age > 26*time.Hour {
		return doctor.Result{Status: doctor.Warn, Message: "The newest backup is older than 26 hours.", Try: "Run spinup backup now --env " + c.Env + "."}
	}
	return doctor.Result{Status: doctor.OK, Message: "The newest backup is less than 26 hours old."}
}

// Capture s_client separately so a successful x509 cannot hide a verification failure.
const certsScript = `certificate=$(openssl s_client -connect "$1:443" -servername "$1" -verify_return_error -verify_hostname "$1" </dev/null 2>/dev/null) || exit 1
printf '%s\n' "$certificate" | openssl x509 -noout -enddate`

func certs(ctx context.Context, c *doctor.Context) doctor.Result {
	r, _, env, skip, ok := remoteRunner(c)
	if !ok {
		return skip
	}
	u, err := url.Parse(env.PublicURL)
	if err != nil || u.Hostname() == "" {
		return doctor.Result{Status: doctor.Skip, Message: "The environment has no public URL."}
	}
	out, err := run(ctx, r, "sh", "-c", certsScript, "doctor-certs", u.Hostname())
	if err != nil {
		return doctor.Result{Status: doctor.Fail, Message: "Cannot verify the TLS certificate.", Try: "Check the public URL certificate chain and hostname."}
	}
	value := strings.TrimPrefix(out, "notAfter=")
	expires, err := time.Parse("Jan 2 15:04:05 2006 MST", value)
	if err != nil {
		return doctor.Result{Status: doctor.Warn, Message: "Cannot read the TLS certificate expiry.", Try: "Check the public URL certificate."}
	}
	days := time.Until(expires).Hours() / 24
	if days < 7 {
		return doctor.Result{Status: doctor.Fail, Message: "The TLS certificate expires in less than 7 days.", Try: "Renew the public URL certificate."}
	}
	if days < 30 {
		return doctor.Result{Status: doctor.Warn, Message: "The TLS certificate expires in less than 30 days.", Try: "Renew the public URL certificate."}
	}
	return doctor.Result{Status: doctor.OK, Message: "The TLS certificate has at least 30 days remaining."}
}

func monitoring(ctx context.Context, c *doctor.Context) doctor.Result {
	r, config, _, skip, ok := remoteRunner(c)
	if !ok {
		return skip
	}
	if !modules.Enabled(config, "monitoring") || config.Monitoring.Profile == "none" {
		return doctor.Result{Status: doctor.Skip, Message: "Monitoring is not enabled."}
	}
	out, err := run(ctx, r, "docker", "compose", "--project-name", config.App.Name+"-monitoring", "ps", "--status", "running", "--services")
	if err != nil || out == "" {
		return doctor.Result{Status: doctor.Fail, Message: "The monitoring stack is not running.", Try: "Run spinup deploy monitoring --env " + c.Env + "."}
	}
	return doctor.Result{Status: doctor.OK, Message: "The monitoring stack is running."}
}
