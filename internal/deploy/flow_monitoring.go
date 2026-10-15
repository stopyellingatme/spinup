package deploy

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// monitoringStack holds the local snapshot used by the monitoring step.
type monitoringStack struct {
	files []releaseFile
	pairs [][2]string
	skip  string
}

func monitoringSettings(c *project.Config) monitoringStack {
	m := c.Monitoring
	return monitoringStack{pairs: [][2]string{
		{"HEARTBEAT_URL", m.HeartbeatURL},
		{"ALERT_EMAIL_TO", m.AlertEmail},
		{"SMTP_HOST", m.SMTPHost},
		{"SMTP_USER", m.SMTPUser},
		{"SMTP_FROM", m.SMTPFrom},
		{"ALERT_NTFY_URL", m.NtfyURL},
		{"PROMETHEUS_RETENTION", m.PrometheusRetention},
		{"PROMETHEUS_RETENTION_SIZE", m.PrometheusRetentionSize},
		{"TEMPO_RETENTION", m.TempoRetention},
	}}
}

// loadMonitoring reads the rendered tree before the deploy changes the VM.
func loadMonitoring(root string, c *project.Config) (monitoringStack, error) {
	m := monitoringSettings(c)
	if !slices.Contains(c.Modules, "monitoring") {
		m.skip = "monitoring module is not selected"
		return m, nil
	}
	lock, err := manifest.Load(filepath.Join(root, project.LockFile))
	if err != nil {
		return m, preflightError("cannot read the monitoring profile: "+stripTry(err.Error()), "restore spinup.lock, or re-render the monitoring pack")
	}
	profile, _ := lock.Answers["monitoring_profile"].(string)
	if profile != c.Monitoring.Profile {
		return m, preflightError(fmt.Sprintf("[monitoring] profile %q differs from the rendered profile %q in spinup.lock", c.Monitoring.Profile, profile), "set [monitoring] profile back to the spinup.lock answer, or re-render the monitoring pack")
	}
	if profile == "none" {
		m.skip = "profile is none"
		return m, nil
	}
	base := filepath.Join(root, "monitoring")
	if info, err := os.Stat(filepath.Join(base, "compose.monitoring.yml")); errors.Is(err, os.ErrNotExist) {
		m.skip = "monitoring/compose.monitoring.yml is missing"
		return m, nil
	} else if err != nil {
		return m, preflightError("cannot inspect monitoring/compose.monitoring.yml: "+err.Error(), "check the monitoring/ directory")
	} else if !info.Mode().IsRegular() {
		return m, preflightError("monitoring/compose.monitoring.yml is not a regular file", "restore monitoring/compose.monitoring.yml")
	}
	err = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// The deployer owns .env. Never upload a local environment or placeholder file.
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		m.files = append(m.files, releaseFile{Path: filepath.ToSlash(rel), Data: data, Mode: info.Mode().Perm()})
		return nil
	})
	if err != nil {
		return m, preflightError("cannot read the monitoring files: "+err.Error(), "check the monitoring/ directory")
	}
	return m, nil
}

// configHashes covers every config mount, including custom rules and dashboards.
// Sort by path so traversal order cannot change a service's Compose definition.
func (m monitoringStack) configHashes() [][2]string {
	files := slices.Clone(m.files)
	slices.SortFunc(files, func(a, b releaseFile) int { return strings.Compare(a.Path, b.Path) })
	var pairs [][2]string
	for _, service := range []struct {
		key  string
		dirs []string
	}{
		{"ALLOY", []string{"alloy/"}},
		{"PROMETHEUS", []string{"prometheus/", "alerts/"}},
		{"LOKI", []string{"loki/"}},
		{"TEMPO", []string{"tempo/"}},
		{"GRAFANA", []string{"grafana/"}},
		{"BLACKBOX", []string{"blackbox/"}},
	} {
		h := sha256.New()
		for _, file := range files {
			if !slices.ContainsFunc(service.dirs, func(dir string) bool { return strings.HasPrefix(file.Path, dir) }) {
				continue
			}
			// Lengths delimit paths and arbitrary file bytes without ambiguity.
			fmt.Fprintf(h, "%d:%s:%o:%d:", len(file.Path), file.Path, file.Mode.Perm(), len(file.Data))
			h.Write(file.Data)
		}
		pairs = append(pairs, [2]string{service.key + "_CONFIG_HASH", fmt.Sprintf("%x", h.Sum(nil))})
	}
	return pairs
}

// env renders literal Compose values. Dollar signs cannot trigger interpolation.
func (m monitoringStack) env(uid, gid, publicURL string) ([]byte, error) {
	pairs := append([][2]string{{"DEPLOY_UID", uid}, {"DEPLOY_GID", gid}, {"PUBLIC_URL", publicURL}}, m.pairs...)
	pairs = append(pairs, m.configHashes()...)
	quote := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`)
	var b strings.Builder
	b.WriteString("# Written by spinup deploy. It holds no secret.\n")
	for _, kv := range pairs {
		if kv[1] == "" {
			continue
		}
		if strings.ContainsAny(kv[1], "\x00\r\n") {
			return nil, preflightError("the monitoring variable "+kv[0]+" contains a NUL or newline", "correct [monitoring] or public_url in spinup.toml")
		}
		fmt.Fprintf(&b, "%s=\"%s\"\n", kv[0], quote.Replace(kv[1]))
	}
	return []byte(b.String()), nil
}

// monitoringSyncScript preserves the inodes of files and directories with bind mounts.
// Hash labels recreate only services whose config changed. Removed files cannot linger as rules.
const monitoringSyncScript = `set -e
[ -d "$1.new" ]
mkdir -p -- "$1"
cp -Rp "$1.new/." "$1/"
find "$1" -type f -exec sh -ec '
  live=$1
  staged=$2
  shift 2
  for file do
    relative=${file#"$live"/}
    if [ ! -f "$staged/$relative" ]; then rm -f -- "$file"; fi
  done
' spinup-monitoring-prune "$1" "$1.new" {} +
rm -rf -- "$1.new"`

// monitoringSecretsScript copies only the secrets used by the monitoring services.
// Keep the directory inode stable because running containers mount it.
const monitoringSecretsScript = `set -e
install -d -m 0700 -- "$2"
find "$2" -mindepth 1 -maxdepth 1 ! -name GRAFANA_ADMIN_PASSWORD ! -name SLACK_WEBHOOK_URL ! -name SMTP_PASSWORD ! -name POSTGRES_PASSWORD -exec rm -rf -- {} +
install -m 0600 -- "$1/GRAFANA_ADMIN_PASSWORD" "$2/GRAFANA_ADMIN_PASSWORD"
for name in SLACK_WEBHOOK_URL SMTP_PASSWORD POSTGRES_PASSWORD; do
  if [ -f "$1/$name" ]; then
    install -m 0600 -- "$1/$name" "$2/$name"
  else
    rm -f -- "$2/$name"
  fi
done`

// monitoring starts the independent monitoring project after the release is recorded.
func (f *flow) monitoring(ctx context.Context) (detail string, err error) {
	m := f.l.monitoring
	if m.skip != "" {
		return "skipped", nil
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("monitoring failed: %w. The release is already recorded. The app is not rolled back", err)
		}
	}()
	data, err := m.env(f.uid, f.gid, f.l.env.PublicURL)
	if err != nil {
		return "", err
	}
	dir := f.root() + "/monitoring"
	staging := dir + ".new"
	if _, err := f.cmd(ctx, false, "rm", "-rf", "--", staging); err != nil {
		return "", fmt.Errorf("clear the monitoring upload: %w", err)
	}
	if _, err := f.cmd(ctx, false, append([]string{"mkdir", "-p", "--"}, releaseDirs(staging, m.files)...)...); err != nil {
		return "", fmt.Errorf("create the monitoring directories: %w", err)
	}
	for _, file := range m.files {
		if err := f.vm.Upload(ctx, file.Data, staging+"/"+file.Path, file.Mode); err != nil {
			return "", fmt.Errorf("upload monitoring/%s: %w", file.Path, err)
		}
	}
	if err := f.vm.Upload(ctx, data, staging+"/.env", 0600); err != nil {
		return "", fmt.Errorf("upload monitoring/.env: %w", err)
	}
	if _, err := f.cmd(ctx, false, "sh", "-c", monitoringSyncScript, "spinup-monitoring", dir); err != nil {
		return "", fmt.Errorf("synchronize the monitoring directory: %w", err)
	}
	if _, err := f.cmd(ctx, false, "sh", "-c", monitoringSecretsScript, "spinup-monitoring-secrets", f.root()+"/secrets", f.root()+"/monitoring-secrets"); err != nil {
		return "", fmt.Errorf("copy the monitoring secrets: %w", err)
	}
	argv := []string{"docker", "compose", "-p", f.l.app + "-monitoring", "--project-directory", dir, "-f", dir + "/compose.monitoring.yml", "up", "-d", "--wait", "--remove-orphans"}
	if _, err := f.vm.Run(ctx, remote.Cmd{Argv: argv, Timeout: 5 * time.Minute, Stdout: f.out, Stderr: f.out, Redact: f.redact}); err != nil {
		return "", fmt.Errorf("start the monitoring stack: %w", brief(err))
	}
	return f.l.app + "-monitoring", nil
}

func (f *flow) planMonitoring() {
	if reason := f.l.monitoring.skip; reason != "" {
		fmt.Fprintf(f.out, "Step monitoring (after record, before post): skipped (%s)\n", reason)
		return
	}
	fmt.Fprintf(f.out, "Step monitoring (after record, before post): upload monitoring/ and .env to %s/monitoring.new.\n", f.root())
	fmt.Fprintf(f.out, "  Copy the monitoring secrets to %s/monitoring-secrets (0700), with file mode 0600.\n", f.root())
	fmt.Fprintf(f.out, "  Synchronize %s/monitoring in place and start Compose project %s-monitoring with up -d --wait --remove-orphans (5 minute timeout).\n", f.root(), f.l.app)
}
