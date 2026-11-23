// Package consoleinstall installs the console and collects facts over SSH.
package consoleinstall

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/remote"
	"go.yaml.in/yaml/v3"
)

// Spec identifies the installed console and its feature selection.
type Spec struct {
	App, Env, Profile, Backend, Origin, Image string
	Jobs                                      bool
}

//go:embed collect.sh
var collector []byte

var appName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var envName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
var imageReference = regexp.MustCompile(`^(?:[a-z0-9]+(?:[.-][a-z0-9]+)*(?::[0-9]{1,5})?/)?[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(?:@sha256:[0-9a-f]{64})?$`)
var originReference = regexp.MustCompile(`^https?://[A-Za-z0-9.\-\[\]:]+/?$`)

var containerID = regexp.MustCompile(`^[0-9a-f]{64}$`)

func failure(action string, err error) error {
	return fmt.Errorf("cannot %s: %w\nTry: run spinup deploy --resume for this environment", action, err)
}

func (s Spec) validate() error {
	if !appName.MatchString(s.App) || !envName.MatchString(s.Env) {
		return fmt.Errorf("invalid app or environment name")
	}
	switch s.Profile {
	case "absent", "none", "lite", "full":
	default:
		return fmt.Errorf("invalid monitoring profile")
	}
	switch s.Backend {
	case "none", "pgbackrest", "pgdump", "walg":
	default:
		return fmt.Errorf("invalid backup backend")
	}
	if len(s.Image) > 512 || !imageReference.MatchString(s.Image) {
		return fmt.Errorf("invalid API image reference")
	}
	if !originReference.MatchString(s.Origin) {
		return fmt.Errorf("invalid app origin")
	}
	u, err := url.Parse(s.Origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(s.Origin, "\x00\r\n\t ") {
		return fmt.Errorf("invalid app origin")
	}
	return nil
}

func (s Spec) active() bool { return s.Profile == "lite" || s.Profile == "full" }

// Validate checks the fixed console topology before deployment changes the VM.
// It accepts YAML formatting changes but requires the protocol's isolation and limits.
func Validate(s Spec, base, overlay []byte) error {
	if err := s.validate(); err != nil {
		return failure("validate console configuration", err)
	}
	if err := validateCompose(s, base, false); err != nil {
		return failure("validate console/compose.console.yml", err)
	}
	if err := validateCompose(s, overlay, true); err != nil {
		return failure("validate console/compose.query.yml", err)
	}
	return nil
}

func validateCompose(s Spec, data []byte, query bool) error {
	expected := baseContract
	if query {
		expected = "services: {}\n"
		if s.active() {
			expected = queryContract
		}
	}
	expected = strings.ReplaceAll(expected, "@APP@", s.App)
	var got, want map[string]any
	if len(data) == 0 || len(data) > 128<<10 {
		return fmt.Errorf("the Compose file is missing or exceeds its limit")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&got); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("expected one Compose document")
	}
	if err := yaml.Unmarshal([]byte(expected), &want); err != nil {
		return err
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("the Compose topology differs from ADR 0037")
	}
	return nil
}

func (s Spec) config() ([]byte, []byte) {
	var b strings.Builder
	quote := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`)
	for _, pair := range [][2]string{{"CONSOLE_IMAGE", s.Image}, {"CONSOLE_APP", s.App}, {"CONSOLE_ENV", s.Env}, {"CONSOLE_PROFILE", s.Profile}, {"CONSOLE_BACKEND", s.Backend}, {"CONSOLE_JOBS", strconv.FormatBool(s.Jobs)}, {"CONSOLE_ORIGIN", s.Origin}} {
		fmt.Fprintf(&b, "%s=\"%s\"\n", pair[0], quote.Replace(pair[1]))
	}
	probe := strings.Join([]string{s.App, s.Env, s.Profile, s.Backend, strconv.FormatBool(s.Jobs), s.Image, s.Origin}, "\n") + "\n"
	return []byte(b.String()), []byte(probe)
}

// prepareScript preserves the data directory and its previous complete snapshot.
const prepareScript = `set -eu
umask 077
[ "$(id -u)" != 0 ]
[ ! -L "$1" ] && [ ! -L "$1/data" ] && [ ! -L "$1/sample.lock" ]
mkdir -p -- "$1/data"
chmod 755 "$1/data"
mktemp -d "$1/.install.XXXXXXXX"`

// publishScript serializes replacement with collectors, without invoking a collector.
const publishScript = `set -eu
umask 077
exec 9> "$1/sample.lock"
chmod 600 "$1/sample.lock"
flock -x 9
for name in compose.console.yml .env probe.conf collect.sh; do
  mv -f -- "$2/$name" "$1/$name"
done`

const timerProbeScript = `if command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then printf 'yes\n'; fi`
const timerDirScript = `set -eu
umask 077
mkdir -p -- "$HOME/.config/systemd/user"
printf '%s\n' "$HOME/.config/systemd/user"`

// Install installs the console files and starts the web service.
// Probe failures do not affect the web service's health.
func Install(ctx context.Context, r remote.Runner, s Spec, base []byte) error {
	if err := s.validate(); err != nil {
		return failure("install console", err)
	}
	if err := validateCompose(s, base, false); err != nil {
		return failure("install console", err)
	}
	dir := "/opt/" + s.App + "/console"
	result, err := r.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", prepareScript, "spinup-console-prepare", dir}})
	if err != nil {
		return failure("prepare console directory", err)
	}
	stage := strings.TrimSpace(string(result.Stdout))
	if !strings.HasPrefix(stage, dir+"/.install.") || strings.ContainsAny(strings.TrimPrefix(stage, dir+"/"), "/\r\n\x00") {
		return failure("prepare console directory", fmt.Errorf("invalid staging directory"))
	}
	defer func() { _, _ = r.Run(context.WithoutCancel(ctx), remote.Cmd{Argv: []string{"rm", "-rf", "--", stage}}) }()
	env, probe := s.config()
	for _, file := range []struct {
		name string
		data []byte
		mode fs.FileMode
	}{{"compose.console.yml", base, 0644}, {".env", env, 0600}, {"probe.conf", probe, 0600}, {"collect.sh", collector, 0700}} {
		if err := r.Upload(ctx, file.data, stage+"/"+file.name, file.mode); err != nil {
			return failure("upload console/"+file.name, err)
		}
	}
	if _, err := r.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", publishScript, "spinup-console-publish", dir, stage}}); err != nil {
		return failure("publish console configuration", err)
	}
	if _, err := r.Run(ctx, remote.Cmd{Argv: compose(s, false, "up", "-d", "--no-deps", "--wait", "console"), Timeout: 5 * time.Minute}); err != nil {
		return failure("start console", err)
	}
	result, err = r.Run(ctx, remote.Cmd{Argv: compose(s, false, "ps", "--all", "--quiet", "console")})
	if err != nil {
		return failure("find console container", err)
	}
	id := strings.TrimSpace(string(result.Stdout))
	if !containerID.MatchString(id) {
		return failure("find console container", fmt.Errorf("missing or ambiguous container reference"))
	}
	result, err = r.Run(ctx, remote.Cmd{Argv: []string{"docker", "inspect", "--type", "container", "--format", `{{json .NetworkSettings.Ports}}`, id}})
	if err != nil {
		return failure("check console port", err)
	}
	var ports map[string][]struct{ HostIP, HostPort string }
	if err := json.Unmarshal(result.Stdout, &ports); err != nil {
		return failure("check console port", err)
	}
	binding := ports["7410/tcp"]
	if len(ports) != 1 || len(binding) != 1 || binding[0].HostIP != "127.0.0.1" || binding[0].HostPort != "7410" {
		return failure("check console port", fmt.Errorf("the Docker engine did not publish only 127.0.0.1:7410:7410/tcp"))
	}
	return installTimer(ctx, r, s, dir)
}

func compose(s Spec, query bool, args ...string) []string {
	dir := "/opt/" + s.App + "/console"
	argv := []string{"docker", "compose", "-p", s.App + "-console", "--project-directory", dir, "-f", dir + "/compose.console.yml"}
	if query {
		argv = append(argv, "-f", dir+"/compose.query.yml")
	}
	return append(argv, args...)
}

func installTimer(ctx context.Context, r remote.Runner, s Spec, dir string) error {
	result, err := r.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", timerProbeScript}})
	if err != nil {
		return failure("inspect console timer", err)
	}
	if strings.TrimSpace(string(result.Stdout)) != "yes" {
		return nil
	}
	result, err = r.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", timerDirScript}})
	if err != nil {
		return failure("prepare console timer", err)
	}
	units := strings.TrimSpace(string(result.Stdout))
	if !strings.HasPrefix(units, "/") || !strings.HasSuffix(units, "/.config/systemd/user") || strings.ContainsAny(units, "\x00\r\n") {
		return failure("prepare console timer", fmt.Errorf("invalid user unit directory"))
	}
	name := "spinup-" + s.App + "-console-sample"
	for _, file := range []struct{ suffix, body string }{{".service", "[Unit]\nDescription=Sample spinup console\n[Service]\nType=oneshot\nExecStart=" + dir + "/collect.sh\n"}, {".timer", "[Unit]\nDescription=Sample spinup console\n[Timer]\nOnActiveSec=5s\nOnUnitActiveSec=30s\nAccuracySec=1s\n[Install]\nWantedBy=timers.target\n"}} {
		if err := r.Upload(ctx, []byte(file.body), units+"/"+name+file.suffix, 0600); err != nil {
			return failure("upload console timer", err)
		}
	}
	for _, args := range [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "--now", name + ".timer"}} {
		if _, err := r.Run(ctx, remote.Cmd{Argv: args}); err != nil {
			return failure("start console timer", err)
		}
	}
	return nil
}

// ReconcileQuery starts or removes the relay after monitoring reconciliation.
func ReconcileQuery(ctx context.Context, r remote.Runner, s Spec, overlay []byte) error {
	if err := s.validate(); err != nil {
		return failure("reconcile console queries", err)
	}
	if err := validateCompose(s, overlay, true); err != nil {
		return failure("reconcile console queries", err)
	}
	if err := r.Upload(ctx, overlay, "/opt/"+s.App+"/console/compose.query.yml", 0644); err != nil {
		return failure("upload console query configuration", err)
	}
	if s.active() {
		if _, err := r.Run(ctx, remote.Cmd{Argv: compose(s, true, "up", "-d", "--no-deps", "--wait", "console-query"), Timeout: 5 * time.Minute}); err != nil {
			return failure("start console query relay", err)
		}
		return nil
	}
	result, err := r.Run(ctx, remote.Cmd{Argv: []string{"docker", "ps", "-a", "--quiet", "--no-trunc", "--filter", "label=com.docker.compose.project=" + s.App + "-console", "--filter", "label=com.docker.compose.service=console-query"}})
	if err != nil {
		return failure("find obsolete console relay", err)
	}
	ids := strings.Fields(string(result.Stdout))
	for _, id := range ids {
		if !containerID.MatchString(id) {
			return failure("find obsolete console relay", fmt.Errorf("invalid container reference"))
		}
	}
	if len(ids) > 0 {
		if _, err := r.Run(ctx, remote.Cmd{Argv: append([]string{"docker", "rm", "-f", "--"}, ids...)}); err != nil {
			return failure("remove obsolete console relay", err)
		}
	}
	return nil
}

// Refresh requests a host sample when the previous sample is old or missing.
func Refresh(ctx context.Context, r remote.Runner, app string) error {
	if !appName.MatchString(app) {
		return failure("refresh console facts", fmt.Errorf("invalid app name"))
	}
	argv := []string{"/opt/" + app + "/console/collect.sh", "--if-old"}
	if _, err := r.Run(ctx, remote.Cmd{Argv: argv, Timeout: time.Minute}); err != nil {
		return failure("refresh console facts", err)
	}
	return nil
}

// StartSample requests an initial sample without delaying deployment under its lock.
func StartSample(ctx context.Context, r remote.Runner, app string) error {
	if !appName.MatchString(app) {
		return failure("refresh console facts", fmt.Errorf("invalid app name"))
	}
	_, err := r.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", initialSampleScript, "spinup-console-sample", "/opt/" + app + "/console/collect.sh"}})
	if err != nil {
		return failure("request console sample", err)
	}
	return nil
}

const initialSampleScript = `nohup "$1" </dev/null >/dev/null 2>&1 &`

// ForceRefresh publishes a new sample after any active sampler completes.
// It releases the sample lock before invoking the collector to avoid recursion.
func ForceRefresh(ctx context.Context, r remote.Runner, app string) error {
	if !appName.MatchString(app) {
		return failure("refresh console facts", fmt.Errorf("invalid app name"))
	}
	_, err := r.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", forceSampleScript, "spinup-console-final-sample", "/opt/" + app + "/console"}, Timeout: time.Minute})
	if err != nil {
		return failure("refresh console facts", err)
	}
	return nil
}

const forceSampleScript = `set -eu
dir=$1
while :; do
  before=$(flock -x "$dir/sample.lock" sh -c 'ls -di "$1/data/facts.json" 2>/dev/null || printf missing' spinup-console-inode "$dir")
  "$dir/collect.sh"
  after=$(ls -di "$dir/data/facts.json" 2>/dev/null || printf missing)
  [ "$after" = missing ] || [ "$before" = "$after" ] || exit 0
  sleep 1
done`

// These fixed topologies enforce the installation boundary in ADR 0037.
const baseContract = `# Managed by spinup. The console runs independently of the app and monitoring.
name: @APP@-console

services:
  console:
    image: ${CONSOLE_IMAGE:?spinup deploy sets CONSOLE_IMAGE} # spinup:allow unpinned-image spinup deploy uses the recorded API image
    entrypoint: ["/console", "serve"]
    user: "65532:65532"
    read_only: true
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    pids_limit: 32
    cpus: "0.25"
    mem_limit: 64m
    environment:
      GOMEMLIMIT: 40MiB
      CONSOLE_APP: ${CONSOLE_APP:?spinup deploy sets CONSOLE_APP}
      CONSOLE_ENV: ${CONSOLE_ENV:?spinup deploy sets CONSOLE_ENV}
      CONSOLE_PROFILE: ${CONSOLE_PROFILE:?spinup deploy sets CONSOLE_PROFILE}
      CONSOLE_ORIGIN: ${CONSOLE_ORIGIN:-}
      CONSOLE_BACKEND: ${CONSOLE_BACKEND:?spinup deploy sets CONSOLE_BACKEND}
      CONSOLE_JOBS: ${CONSOLE_JOBS:?spinup deploy sets CONSOLE_JOBS}
    ports:
      - "127.0.0.1:7410:7410/tcp"
    volumes:
      - /opt/@APP@/console/data:/data:ro
    networks: [console]
    healthcheck:
      test: ["CMD", "/console", "healthcheck"]
      interval: 15s
      timeout: 3s
      retries: 3
    restart: unless-stopped
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"

networks:
  console:
    name: @APP@-console
    driver: bridge
`
const queryContract = `# Managed by spinup. Start the relay after the monitoring network exists.
services:
  console-query:
    image: ${CONSOLE_IMAGE:?spinup deploy sets CONSOLE_IMAGE} # spinup:allow unpinned-image spinup deploy uses the recorded API image
    entrypoint: ["/console", "query"]
    profiles: [query]
    user: "65532:65532"
    read_only: true
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    pids_limit: 32
    cpus: "0.25"
    mem_limit: 48m
    environment:
      GOMEMLIMIT: 28MiB
      CONSOLE_APP: ${CONSOLE_APP:?spinup deploy sets CONSOLE_APP}
      CONSOLE_ENV: ${CONSOLE_ENV:?spinup deploy sets CONSOLE_ENV}
      CONSOLE_PROFILE: ${CONSOLE_PROFILE:?spinup deploy sets CONSOLE_PROFILE}
    networks: [console, monitoring]
    healthcheck:
      test: ["CMD", "/console", "healthcheck", "query"]
      interval: 15s
      timeout: 3s
      retries: 3
    restart: unless-stopped
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"

networks:
  monitoring:
    name: @APP@-monitoring
    external: true
`
