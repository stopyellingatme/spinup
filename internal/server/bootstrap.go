package server

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// StepResult reports whether a bootstrap step needs a change.
type StepResult struct {
	Step    string `json:"step"`
	Changed bool   `json:"changed"`
	Planned bool   `json:"planned"`
}

type bootstrapStep struct{ name, check, apply string }

// BootstrapHost checks each step before changing a Debian or Ubuntu host.
// Plan mode executes only read-only probes. The caller supplies a validated public key.
func BootstrapHost(ctx context.Context, r remote.Runner, app, key string, plan bool) ([]StepResult, error) {
	if !appName.MatchString(app) {
		return nil, fmt.Errorf("bootstrap failed: invalid app name\nTry: correct app.name in spinup.toml")
	}
	result, err := r.Run(ctx, remote.Cmd{Argv: []string{"cat", "/etc/os-release"}})
	if err != nil {
		return nil, fmt.Errorf("detect server OS: %w\nTry: use a Debian or Ubuntu host", err)
	}
	id := ""
	for line := range strings.SplitSeq(string(result.Stdout), "\n") {
		if value, ok := strings.CutPrefix(line, "ID="); ok {
			id = strings.Trim(value, "\"'")
		}
	}
	if id != "debian" && id != "ubuntu" {
		return nil, fmt.Errorf("bootstrap failed: unsupported OS %q\nTry: use Debian or Ubuntu", id)
	}
	if _, err := r.Run(ctx, remote.Cmd{Argv: []string{"sudo", "-n", "true"}}); err != nil {
		return nil, fmt.Errorf("bootstrap requires passwordless sudo: %w\nTry: connect as a user with passwordless sudo", err)
	}
	// Read the server-side connection port before sudo removes SSH_CONNECTION.
	// This also covers ports selected through the user's SSH configuration.
	connection, err := r.Run(ctx, remote.Cmd{Argv: []string{"sh", "-ec", `printf '%s\n' "${SSH_CONNECTION-}"`}})
	if err != nil {
		return nil, fmt.Errorf("read SSH connection port: %w\nTry: check the remote SSH_CONNECTION environment", err)
	}
	fields := strings.Fields(string(connection.Stdout))
	if len(fields) != 4 {
		return nil, fmt.Errorf("read SSH connection port: SSH_CONNECTION must contain four fields\nTry: connect through SSH with SSH_CONNECTION available")
	}
	port, err := strconv.Atoi(fields[3])
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("read SSH connection port: invalid server port\nTry: check the remote SSH_CONNECTION environment")
	}
	reports := make([]StepResult, 0)
	for _, step := range bootstrapSteps(app, key) {
		if step.name == "firewall" {
			prefix := "connection_port=" + remote.Quote(strconv.Itoa(port)) + "\n"
			step.check = prefix + step.check
			step.apply = prefix + step.apply
		}
		// Tools such as systemctl use several exit codes for unmet conditions.
		// Normalize probe failures while preserving SSH and sudo failures.
		probe := "(\n" + step.check + "\n) || exit 1"
		_, err := r.Run(ctx, remote.Cmd{Argv: []string{"sudo", "-n", "sh", "-ec", probe}})
		changed := false
		if err != nil {
			var exit *runexec.ExitError
			if !errors.As(err, &exit) || exit.Code != 1 {
				return reports, fmt.Errorf("probe bootstrap step %s: %w\nTry: check SSH access and passwordless sudo", step.name, err)
			}
			changed = true
			if !plan {
				if _, err := r.Run(ctx, remote.Cmd{Argv: []string{"sudo", "-n", "sh", "-ec", step.apply}}); err != nil {
					return reports, fmt.Errorf("bootstrap step %s: %w\nTry: correct the reported error and repeat server bootstrap", step.name, err)
				}
			}
		}
		reports = append(reports, StepResult{Step: step.name, Changed: changed, Planned: plan})
	}
	return reports, nil
}

func fileCheck(path, content string) string {
	return "printf '%s' " + remote.Quote(content) + " | cmp -s - " + remote.Quote(path)
}
func writeFile(path, content string) string {
	return "printf '%s' " + remote.Quote(content) + " > " + remote.Quote(path) + "\nchmod 0644 " + remote.Quote(path) + "\n"
}

func bootstrapSteps(app, key string) []bootstrapStep {
	packages := "ca-certificates curl gnupg python3 ufw fail2ban unattended-upgrades"
	packageCheck := func(names string) string {
		return "for p in " + names + "; do [ \"$(dpkg-query -W -f='${Status}' \"$p\" 2>/dev/null)\" = 'install ok installed' ] || exit 1; done\n"
	}
	sshConfig := "PermitRootLogin no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\nAuthenticationMethods publickey\n"
	sshEffective := "/usr/sbin/sshd -T | awk '$1 == \"permitrootlogin\" {r=($2==\"no\")} $1 == \"passwordauthentication\" {p=($2==\"no\")} $1 == \"kbdinteractiveauthentication\" {k=($2==\"no\")} $1 == \"pubkeyauthentication\" {u=($2==\"yes\")} $1 == \"authenticationmethods\" {a=($2==\"publickey\")} END {exit !(r && p && k && u && a)}'"
	sshApply := `backup=$(mktemp -d /etc/ssh/spinup.XXXXXX)
cp -p /etc/ssh/sshd_config "$backup/main"
mkdir -p /etc/ssh/sshd_config.d
if [ -e /etc/ssh/sshd_config.d/00-spinup.conf ]; then cp -p /etc/ssh/sshd_config.d/00-spinup.conf "$backup/dropin"; fi
ok=false
trap 'if [ "$ok" != true ]; then cp -p "$backup/main" /etc/ssh/sshd_config; if [ -e "$backup/dropin" ]; then cp -p "$backup/dropin" /etc/ssh/sshd_config.d/00-spinup.conf; else rm -f /etc/ssh/sshd_config.d/00-spinup.conf; fi; fi; rm -rf "$backup"' 0
` + writeFile("/etc/ssh/sshd_config.d/00-spinup.conf", sshConfig) + `if [ "$(head -n 1 /etc/ssh/sshd_config)" != 'Include /etc/ssh/sshd_config.d/00-spinup.conf' ]; then
 { printf '%s\n' 'Include /etc/ssh/sshd_config.d/00-spinup.conf'; cat "$backup/main"; } > /etc/ssh/sshd_config
fi
/usr/sbin/sshd -t
` + sshEffective + "\nsystemctl reload ssh\nok=true\n"
	firewallCheck := firewallSSHPorts + `command -v ufw >/dev/null || exit 1
status=$(ufw status verbose)
printf '%s\n' "$status" | grep -q '^Status: active$' || exit 1
printf '%s\n' "$status" | grep -Fq 'Default: deny (incoming), allow (outgoing)' || exit 1
for p in $ssh_ports; do
 printf '%s\n' "$status" | grep -Eq "^$p/tcp[[:space:]]+ALLOW IN[[:space:]]+Anywhere" || exit 1
done
for port in 80/tcp 443/tcp 443/udp; do
 printf '%s\n' "$status" | grep -Eq "^$port[[:space:]]+ALLOW IN[[:space:]]+Anywhere" || exit 1
done`
	jail := "[sshd]\nenabled = true\nbackend = systemd\nmaxretry = 5\nbantime = 1h\n"
	upgrades := "APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"1\";\n"
	sysctl := "vm.swappiness = 10\nnet.core.somaxconn = 4096\nnet.ipv4.tcp_syncookies = 1\n"
	dockerPackages := "docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin"
	root := "/opt/" + app
	dirs := []string{root, root + "/releases", root + "/backups", root + "/data", root + "/caddy", root + "/postgres", root + "/postgres/conf.d", root + "/data/caddy", root + "/data/caddy/data", root + "/data/caddy/config"}
	layoutCheck, layoutApply := "", ""
	for _, dir := range append(dirs, root+"/secrets") {
		mode := "750"
		if dir == root+"/secrets" {
			mode = "700"
		}
		layoutCheck += "[ -d " + remote.Quote(dir) + " ] && [ \"$(stat -c '%U:%G:%a' " + remote.Quote(dir) + ")\" = 'deploy:deploy:" + mode + "' ] || exit 1\n"
		layoutApply += "install -d -o deploy -g deploy -m 0" + mode + " " + remote.Quote(dir) + "\n"
	}
	sudoers := backupSudoers(app)
	sudoersPath := "/etc/sudoers.d/spinup-" + app
	sudoersApply := `mkdir -p /etc/sudoers.d
tmp=$(mktemp /etc/sudoers.d/.spinup.XXXXXX)
trap 'rm -f "$tmp"' 0
trap 'exit 1' HUP INT TERM
` + "printf '%s' " + remote.Quote(sudoers) + ` > "$tmp"
chmod 0440 "$tmp"
visudo -cf "$tmp"
` + `mv -f "$tmp" ` + remote.Quote(sudoersPath)
	return []bootstrapStep{
		{"packages", packageCheck(packages), "apt-get update\nDEBIAN_FRONTEND=noninteractive apt-get install -y " + packages},
		{"deploy-user", "id deploy >/dev/null 2>&1 && [ \"$(getent passwd deploy | cut -d: -f6-7)\" = '/home/deploy:/bin/bash' ] && grep -Fqx -- " + remote.Quote(key) + " /home/deploy/.ssh/authorized_keys && [ \"$(stat -c '%U:%G:%a' /home/deploy/.ssh)\" = 'deploy:deploy:700' ] && [ \"$(stat -c '%U:%G:%a' /home/deploy/.ssh/authorized_keys)\" = 'deploy:deploy:600' ]", `if ! id deploy >/dev/null 2>&1; then useradd --create-home --shell /bin/bash deploy; fi
usermod --home /home/deploy --shell /bin/bash --lock deploy
install -d -o deploy -g deploy -m 0700 /home/deploy/.ssh
touch /home/deploy/.ssh/authorized_keys
` + "if ! grep -Fqx -- " + remote.Quote(key) + " /home/deploy/.ssh/authorized_keys; then printf '\\n%s\\n' " + remote.Quote(key) + " >> /home/deploy/.ssh/authorized_keys; fi\nchown deploy:deploy /home/deploy/.ssh/authorized_keys\nchmod 0600 /home/deploy/.ssh/authorized_keys"},
		{"backup-sudo", fileCheck(sudoersPath, sudoers) + " && [ \"$(stat -c '%U:%G:%a' " + remote.Quote(sudoersPath) + ")\" = 'root:root:440' ] && visudo -cf " + remote.Quote(sudoersPath), sudoersApply},
		{"sshd", sshEffective, sshApply},
		{"firewall", firewallCheck, firewallSSHPorts + "ufw default deny incoming\nufw default allow outgoing\nfor p in $ssh_ports; do ufw allow \"$p/tcp\" || exit 1; done\nufw allow 80/tcp\nufw allow 443/tcp\nufw allow 443/udp\nufw --force enable"},
		{"fail2ban", fileCheck("/etc/fail2ban/jail.d/sshd.local", jail) + " && systemctl is-enabled --quiet fail2ban && systemctl is-active --quiet fail2ban", "mkdir -p /etc/fail2ban/jail.d\n" + writeFile("/etc/fail2ban/jail.d/sshd.local", jail) + "systemctl enable --now fail2ban\nsystemctl restart fail2ban"},
		{"unattended-upgrades", fileCheck("/etc/apt/apt.conf.d/20auto-upgrades", upgrades) + " && systemctl is-enabled --quiet unattended-upgrades && systemctl is-active --quiet unattended-upgrades", writeFile("/etc/apt/apt.conf.d/20auto-upgrades", upgrades) + "systemctl enable --now unattended-upgrades\nsystemctl enable --now apt-daily-upgrade.timer"},
		{"docker-repository", dockerRepositoryCheck, dockerRepositoryApply},
		{"docker", packageCheck(dockerPackages) + "docker compose version >/dev/null && systemctl is-enabled --quiet docker && systemctl is-active --quiet docker && id -nG deploy | tr ' ' '\\n' | grep -qx docker", "apt-get update\nDEBIAN_FRONTEND=noninteractive apt-get install -y " + dockerPackages + "\nusermod -aG docker deploy\nsystemctl enable --now docker"},
		{"docker-logs", dockerLogCheck, dockerLogApply},
		{"swap", "[ -e /swapfile ] || awk 'END {exit !(NR > 1)}' /proc/swaps", `ram=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)
[ "$ram" -gt 0 ]
umask 077
fallocate -l "${ram}K" /swapfile || dd if=/dev/zero of=/swapfile bs=1M count="$(((ram+1023)/1024))"
chmod 0600 /swapfile
mkswap /swapfile
swapon /swapfile
if ! grep -Eq '^/swapfile[[:space:]]' /etc/fstab; then printf '%s\n' '/swapfile none swap sw 0 0' >> /etc/fstab; fi`},
		{"sysctl", fileCheck("/etc/sysctl.d/90-spinup.conf", sysctl) + " && [ \"$(sysctl -n vm.swappiness)\" = 10 ] && [ \"$(sysctl -n net.core.somaxconn)\" = 4096 ] && [ \"$(sysctl -n net.ipv4.tcp_syncookies)\" = 1 ]", writeFile("/etc/sysctl.d/90-spinup.conf", sysctl) + "sysctl -p /etc/sysctl.d/90-spinup.conf"},
		{"layout", layoutCheck, layoutApply},
	}
}

const dockerRepositoryCheck = `. /etc/os-release
expected="deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/$ID $VERSION_CODENAME stable"
[ -r /etc/apt/keyrings/docker.asc ] && [ "$(stat -c '%a' /etc/apt/keyrings/docker.asc)" = 644 ] && [ "$(cat /etc/apt/sources.list.d/docker.list)" = "$expected" ] || exit 1
gpg --batch --no-options --homedir /etc/apt/keyrings --no-keyring --trust-model always --no-autostart --lock-never --no-random-seed-file --show-keys --with-colons /etc/apt/keyrings/docker.asc 2>/dev/null | awk -F: '$1=="fpr" && $10=="9DC858229FC7DD38854AE2D88D81803C0EBFCD88" {ok=1} END {exit !ok}'`

const dockerRepositoryApply = `. /etc/os-release
install -d -m 0755 /etc/apt/keyrings
key=$(mktemp /etc/apt/keyrings/docker.XXXXXX)
trap 'rm -f "$key"' 0
curl -fsSL "https://download.docker.com/linux/$ID/gpg" -o "$key"
gpg --batch --no-options --homedir /etc/apt/keyrings --no-keyring --trust-model always --no-autostart --lock-never --no-random-seed-file --show-keys --with-colons "$key" | awk -F: '$1=="fpr" && $10=="9DC858229FC7DD38854AE2D88D81803C0EBFCD88" {ok=1} END {exit !ok}'
chmod 0644 "$key"
mv "$key" /etc/apt/keyrings/docker.asc
printf '%s\n' "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/$ID $VERSION_CODENAME stable" > /etc/apt/sources.list.d/docker.list`

const dockerLogCheck = `command -v python3 >/dev/null || exit 1
[ -f /etc/docker/daemon.json ] || exit 1
python3 -c 'import json,sys; d=json.load(open("/etc/docker/daemon.json")); sys.exit(0 if d.get("log-driver")=="json-file" and d.get("log-opts",{}).get("max-size")=="10m" and d.get("log-opts",{}).get("max-file")=="3" else 1)'`

const dockerLogApply = `mkdir -p /etc/docker
config=$(mktemp /etc/docker/daemon.XXXXXX)
trap 'rm -f "$config"' 0
python3 - "$config" <<'PY'
import json, os, sys
path = "/etc/docker/daemon.json"
data = {}
if os.path.exists(path):
    with open(path) as f:
        data = json.load(f)
data["log-driver"] = "json-file"
data.setdefault("log-opts", {}).update({"max-size": "10m", "max-file": "3"})
with open(sys.argv[1], "w") as f:
    json.dump(data, f, sort_keys=True, indent=2)
    f.write("\n")
PY
chmod 0644 "$config"
dockerd --validate --config-file="$config"
mv "$config" /etc/docker/daemon.json
systemctl restart docker`

// firewallSSHPorts discovers and validates every port before changing UFW.
// Capture sshd output separately so a failed sshd cannot be hidden by awk.
const firewallSSHPorts = `sshd_config=$(/usr/sbin/sshd -T) || exit 1
ssh_ports=$(printf '%s\n' "$sshd_config" | awk '$1=="port"{print $2}')
if [ -n "${connection_port:-}" ]; then ssh_ports="$ssh_ports $connection_port"; fi
[ -n "$ssh_ports" ] || exit 1
for p in $ssh_ports; do
 case "$p" in ''|*[!0-9]*) exit 1 ;; esac
 [ "$p" -ge 1 ] && [ "$p" -le 65535 ] || exit 1
done
`

// backupSudoers permits only this app's backup units and fixed staging paths.
func backupSudoers(app string) string {
	commands := []string{"/usr/bin/systemctl daemon-reload"}
	for _, kind := range []string{"full", "diff", "verify"} {
		timer := "spinup-" + app + "-backup-" + kind + ".timer"
		for _, action := range []string{"enable", "disable", "start", "stop"} {
			commands = append(commands, "/usr/bin/systemctl "+action+" --now "+timer)
		}
		for _, ext := range []string{"service", "timer"} {
			name := "spinup-" + app + "-backup-" + kind + "." + ext
			commands = append(commands, "/usr/bin/install -m 0644 /opt/"+app+"/backups/"+name+" /etc/systemd/system/"+name)
		}
	}
	// backup schedule --remove deletes the six unit files with one rm, in this order.
	remove := []string{"/usr/bin/rm -f"}
	for _, kind := range []string{"full", "diff", "verify"} {
		unit := "/etc/systemd/system/spinup-" + app + "-backup-" + kind
		remove = append(remove, unit+".timer", unit+".service")
	}
	commands = append(commands, strings.Join(remove, " "))
	return "deploy ALL=(root) NOPASSWD: " + strings.Join(commands, ", ") + "\n"
}
