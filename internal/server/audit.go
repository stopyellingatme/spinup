package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/stopyellingatme/spinup/internal/doctor/composelint"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// CheckResult holds one audit result without inspected environment values.
type CheckResult struct {
	Check     string `json:"check"`
	Container string `json:"container,omitempty"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	Try       string `json:"try,omitempty"`
}

func check(id, container, message, try string, safe bool) CheckResult {
	r := CheckResult{Check: id, Container: container, Status: "ok", Message: message}
	if !safe {
		r.Status = "fail"
		r.Try = try
	}
	return r
}

// Audit reports unsafe host settings and returns exit code 1 on any failure.
func Audit(ctx context.Context, o Options, runner runexec.Runner) error {
	t, err := load(o)
	if err != nil {
		return err
	}
	r := remote.New(t.host, runner, remote.Options{AcceptNewHostKey: o.AcceptNewHostKey})
	defer func() { _ = r.Close(ctx) }()
	results := AuditHost(ctx, r, t.app)
	return writeAudit(results, o)
}

func writeAudit(results []CheckResult, o Options) error {
	failed := false
	for _, result := range results {
		if result.Status == "fail" {
			failed = true
		}
		if !o.JSON {
			name := result.Check
			if result.Container != "" {
				name += ":" + result.Container
			}
			if _, err := fmt.Fprintf(output(o.Out), "%s\t%s\t%s\n", result.Status, name, result.Message); err != nil {
				return fmt.Errorf("write audit report: %w\nTry: check the output stream", err)
			}
			if result.Try != "" {
				if _, err := fmt.Fprintf(output(o.Out), "Try: %s\n", result.Try); err != nil {
					return fmt.Errorf("write audit guidance: %w\nTry: check the output stream", err)
				}
			}
		}
	}
	if o.JSON {
		if err := json.NewEncoder(output(o.Out)).Encode(results); err != nil {
			return fmt.Errorf("write audit JSON: %w\nTry: check the output stream", err)
		}
	}
	if failed {
		return &ui.Error{What: "Server audit failed", Why: "the host has unsafe or unreadable settings", Try: "correct the reported failures and repeat server audit", Code: ui.ExitError}
	}
	return nil
}

// AuditHost checks SSH, every running container, disk space, and automatic upgrades.
// Failed probes become failed checks. Raw inspect output never enters a report.
func AuditHost(ctx context.Context, r remote.Runner, app string) []CheckResult {
	sshd, sshErr := r.Run(ctx, remote.Cmd{Argv: []string{"sudo", "-n", "/usr/sbin/sshd", "-T"}})
	options := sshOptions(string(sshd.Stdout))
	keysOnly := sshErr == nil && options["passwordauthentication"] == "no" && options["kbdinteractiveauthentication"] == "no" && options["pubkeyauthentication"] == "yes"
	// AuthenticationMethods defaults to "any" on hosts without a managed configuration.
	// Disabled password, keyboard-interactive, host-based, and GSSAPI auth also imply keys only.
	keysOnly = keysOnly && (options["authenticationmethods"] == "publickey" || (options["hostbasedauthentication"] == "no" && options["gssapiauthentication"] == "no"))
	results := []CheckResult{
		check("ssh-keys-only", "", "SSH must permit only public key authentication.", "Disable password, keyboard-interactive, host-based, and GSSAPI authentication. Enable public keys.", keysOnly),
		check("ssh-root-login", "", "SSH root login must be disabled.", "Set PermitRootLogin no. Run sshd -t before reloading SSH.", sshErr == nil && options["permitrootlogin"] == "no"),
	}
	results = append(results, auditContainers(ctx, r)...)
	disk, err := r.Run(ctx, remote.Cmd{Argv: []string{"df", "-Pk", "/opt/" + app}})
	results = append(results, check("disk-space", "", "The application filesystem must have more than 15% free space.", "Free disk space or expand the application filesystem.", err == nil && diskSafe(string(disk.Stdout))))
	upgrades, err := r.Run(ctx, remote.Cmd{Argv: []string{"systemctl", "is-active", "unattended-upgrades"}})
	results = append(results, check("unattended-upgrades", "", "Unattended upgrades must be active.", "Enable unattended-upgrades with systemctl enable --now unattended-upgrades.", err == nil && strings.TrimSpace(string(upgrades.Stdout)) == "active"))
	return results
}

func sshOptions(text string) map[string]string {
	options := make(map[string]string)
	for line := range strings.SplitSeq(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			options[fields[0]] = fields[1]
		}
	}
	return options
}

func diskSafe(text string) bool {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) != 2 {
		return false
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 6 {
		return false
	}
	used, e1 := strconv.ParseFloat(fields[2], 64)
	available, e2 := strconv.ParseFloat(fields[3], 64)
	return e1 == nil && e2 == nil && used >= 0 && available > 0 && available/(used+available) > 0.15
}

func auditContainers(ctx context.Context, r remote.Runner) []CheckResult {
	failure := func(message string) []CheckResult {
		return []CheckResult{check("containers", "", message, "Check Docker access and repeat server audit.", false)}
	}
	ps, err := r.Run(ctx, remote.Cmd{Argv: []string{"docker", "ps", "-q"}})
	if err != nil {
		return failure("Cannot list running containers.")
	}
	ids := strings.Fields(string(ps.Stdout))
	if len(ids) == 0 {
		return []CheckResult{{Check: "containers", Status: "skip", Message: "No running containers."}}
	}
	for _, id := range ids {
		if !containerID(id) {
			return failure("Docker returned an invalid container ID.")
		}
	}
	sort.Strings(ids)
	inspected, err := r.Run(ctx, remote.Cmd{Argv: append([]string{"docker", "inspect"}, ids...)})
	if err != nil {
		return failure("Cannot inspect running containers.")
	}
	return InspectChecks(inspected.Stdout)
}

func containerID(id string) bool {
	for _, c := range id {
		if (c < 'a' || c > 'f') && (c < '0' || c > '9') {
			return false
		}
	}
	return len(id) >= 6 && len(id) <= 64
}

type portBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string
}
type containerInspect struct {
	Name, ID string
	Config   *struct {
		User, Image string
		Env         []string
		Labels      map[string]string
	}
	HostConfig *struct {
		ReadonlyRootfs               bool
		Privileged                   bool
		CapDrop, CapAdd, SecurityOpt []string
		NetworkMode                  string
		PortBindings                 map[string][]portBinding
	}
	NetworkSettings *struct{ Ports map[string][]portBinding }
}

// InspectChecks evaluates docker inspect JSON with the Compose security rule names.
// It returns only container names and fixed messages. It never returns secret values.
func InspectChecks(data []byte) []CheckResult {
	invalid := func() []CheckResult {
		return []CheckResult{check("containers", "", "Docker inspect returned incomplete or invalid JSON.", "Check docker inspect output and Docker access.", false)}
	}
	var containers []containerInspect
	if err := json.Unmarshal(data, &containers); err != nil || len(containers) == 0 {
		return invalid()
	}
	sort.Slice(containers, func(i, j int) bool { return containers[i].Name < containers[j].Name })
	results := make([]CheckResult, 0, len(containers)*7)
	for _, c := range containers {
		if c.Config == nil || c.HostConfig == nil || c.NetworkSettings == nil {
			return invalid()
		}
		name := strings.TrimPrefix(c.Name, "/")
		if name == "" {
			name = c.ID
		}
		service := c.Config.Labels["com.docker.compose.service"]
		postgres := service == "postgres" || strings.Contains(strings.ToLower(c.Config.Image), "postgres")
		publishedPostgres, public := false, c.HostConfig.NetworkMode == "host"
		for _, ports := range []map[string][]portBinding{c.HostConfig.PortBindings, c.NetworkSettings.Ports} {
			for port, bindings := range ports {
				if len(bindings) > 0 && (postgres || strings.HasPrefix(port, "5432/")) {
					publishedPostgres = true
				}
				for _, binding := range bindings {
					if binding.HostIP != "127.0.0.1" && !caddyPort(service, port, binding.HostPort) {
						public = true
					}
				}
			}
		}
		if postgres && c.HostConfig.NetworkMode == "host" {
			publishedPostgres = true
		}
		results = append(results,
			check("postgres-port", name, "Postgres must not publish a port.", "Remove every Postgres port mapping and host networking.", !publishedPostgres),
			check("public-port", name, "Only Caddy ports 80 and 443 may be public.", "Bind published ports to 127.0.0.1. Publish only Caddy ports 80 and 443.", !public),
			check("read-only", name, "The container filesystem must be read-only.", "Set read_only: true and use tmpfs for writable paths.", c.HostConfig.ReadonlyRootfs),
			check("cap-drop", name, "The container must drop all Linux capabilities.", "Set cap_drop: [ALL] and remove cap_add and privileged mode.", contains(c.HostConfig.CapDrop, "ALL") && len(c.HostConfig.CapAdd) == 0 && !c.HostConfig.Privileged),
			check("no-new-privileges", name, "The container must prevent new privileges.", "Set security_opt: [no-new-privileges:true].", noNewPrivileges(c.HostConfig.SecurityOpt) && !c.HostConfig.Privileged),
			check("non-root", name, "The container must use a non-root user.", "Set an explicit non-root user in Compose or the image.", nonRoot(c.Config.User)),
			check("literal-secret", name, "The container environment must contain no literal secrets.", "Mount secret files and use _FILE variables.", !inspectSecret(c.Config.Env)),
		)
		if service == "grafana" || strings.Contains(strings.ToLower(c.Config.Image), "grafana/grafana") {
			anonymous := false
			for _, env := range c.Config.Env {
				key, value, _ := strings.Cut(env, "=")
				if key == "GF_AUTH_ANONYMOUS_ENABLED" {
					enabled, _ := strconv.ParseBool(strings.TrimSpace(value))
					anonymous = enabled
				}
			}
			results = append(results, check("grafana-auth", name, "Grafana must require authentication.", "Disable anonymous access to Grafana and use an SSH tunnel.", !anonymous))
		}
	}
	return results
}

func caddyPort(service, port, hostPort string) bool {
	return service == "caddy" && (port == "80/tcp" && hostPort == "80" || (port == "443/tcp" || port == "443/udp") && hostPort == "443")
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if strings.EqualFold(v, value) {
			return true
		}
	}
	return false
}
func noNewPrivileges(options []string) bool {
	for _, option := range options {
		if option == "no-new-privileges" || option == "no-new-privileges:true" || option == "no-new-privileges=true" {
			return true
		}
	}
	return false
}
func nonRoot(user string) bool {
	user, _, _ = strings.Cut(user, ":")
	if user == "" || user == "root" || strings.TrimSpace(user) != user {
		return false
	}
	id, err := strconv.ParseUint(user, 10, 64)
	return err != nil || id != 0
}
func inspectSecret(environment []string) bool {
	// Inspect values are already expanded. Every nonempty value is literal here.
	// Reuse the lint name rules without passing inspected values to the YAML parser.
	redacted := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if found && value != "" {
			redacted = append(redacted, key+"=present")
		}
	}
	data, err := json.Marshal(map[string]any{"services": map[string]any{"container": map[string]any{"environment": redacted}}})
	if err != nil {
		return true
	}
	findings, err := composelint.Lint("deploy/compose.yml", data)
	if err != nil {
		return true
	}
	for _, finding := range findings {
		if finding.Rule == "literal-secret" {
			return true
		}
	}
	// Inspection sees expanded values. A URL password remains a literal even if it contains '$'.
	for _, entry := range environment {
		_, value, _ := strings.Cut(entry, "=")
		parsed, err := url.Parse(value)
		if err == nil && parsed.User != nil {
			if password, ok := parsed.User.Password(); ok && password != "" {
				return true
			}
		}
	}
	return false
}
