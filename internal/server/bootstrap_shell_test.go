package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

func TestBootstrapShellSyntax(t *testing.T) {
	t.Parallel()
	for _, step := range bootstrapSteps("todo", "ssh-ed25519 AAAA comment ' $(false)") {
		t.Run(step.name, func(t *testing.T) {
			t.Parallel()
			for _, script := range []string{step.check, step.apply} {
				if _, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "sh", Args: []string{"-n"}, Stdin: strings.NewReader(script)}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSSHDValidationBeforeReload(t *testing.T) {
	t.Parallel()
	for _, invalid := range []bool{true, false} {
		t.Run(map[bool]string{true: "invalid", false: "valid"}[invalid], func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			sshDir := filepath.Join(dir, "ssh")
			if err := os.MkdirAll(filepath.Join(sshDir, "sshd_config.d"), 0755); err != nil {
				t.Fatal(err)
			}
			main := filepath.Join(sshDir, "sshd_config")
			dropin := filepath.Join(sshDir, "sshd_config.d", "00-spinup.conf")
			original := "# operator configuration\nPermitRootLogin yes\n"
			for path, data := range map[string]string{
				main: original, dropin: "# old drop-in\n",
				filepath.Join(dir, "sshd"):      "#!/bin/sh\nif [ \"$1\" = -t ]; then printf 'validate\\n' >> \"$EVENTS\"; exit \"$INVALID\"; fi\nprintf '%s' '" + safeSSHD + "'\n",
				filepath.Join(dir, "systemctl"): "#!/bin/sh\nprintf 'reload\\n' >> \"$EVENTS\"\n",
			} {
				if err := runexec.WriteExecutable(path, []byte(data), 0700); err != nil {
					t.Fatal(err)
				}
			}
			var step bootstrapStep
			for _, s := range bootstrapSteps("todo", "key") {
				if s.name == "sshd" {
					step = s
				}
			}
			rewrite := func(script string) string {
				script = strings.ReplaceAll(script, "/etc/ssh", sshDir)
				script = strings.ReplaceAll(script, "/usr/sbin/sshd", filepath.Join(dir, "sshd"))
				return strings.ReplaceAll(script, "systemctl reload ssh", filepath.Join(dir, "systemctl")+" reload ssh")
			}
			invalidValue := "0"
			if invalid {
				invalidValue = "1"
			}
			events := filepath.Join(dir, "events")
			env := []string{"EVENTS=" + events, "INVALID=" + invalidValue}
			_, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "sh", Args: []string{"-ec", rewrite(step.apply)}, Env: env})
			if (err != nil) != invalid {
				t.Fatalf("apply error = %v", err)
			}
			data, err := os.ReadFile(events)
			if err != nil {
				t.Fatal(err)
			}
			want := "validate\nreload\n"
			if invalid {
				want = "validate\n"
			}
			if string(data) != want {
				t.Fatalf("events = %q, want %q", data, want)
			}
			gotMain, err := os.ReadFile(main)
			if err != nil {
				t.Fatal(err)
			}
			gotDropin, err := os.ReadFile(dropin)
			if err != nil {
				t.Fatal(err)
			}
			if invalid {
				if string(gotMain) != original || string(gotDropin) != "# old drop-in\n" {
					t.Fatal("failed validation did not restore SSH files")
				}
			} else {
				if !strings.HasPrefix(string(gotMain), "Include "+dropin) || !strings.Contains(string(gotDropin), "PermitRootLogin no") {
					t.Fatal("hardening configuration missing")
				}
				if _, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "sh", Args: []string{"-ec", rewrite(step.check)}, Env: env}); err != nil {
					t.Fatal(err)
				}
			}
			leftovers, err := filepath.Glob(filepath.Join(sshDir, "spinup.*"))
			if err != nil || len(leftovers) != 0 {
				t.Fatalf("SSH backups remain: %v, %v", leftovers, err)
			}
		})
	}
}

func TestBootstrapFirewallSSHPorts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, ports, connected, want string
		sshdFails, fails             bool
	}{
		{name: "custom port", ports: "port 2222\n", want: "2222"},
		{name: "multiple ports", ports: "port 2222\nport 2200\n", want: "2222 2200"},
		{name: "connection port", ports: "port 22\n", connected: "2222", want: "22 2222"},
		{name: "connection only", connected: "2222", want: "2222"},
		{name: "empty ports", fails: true},
		{name: "sshd failure", connected: "2222", sshdFails: true, fails: true},
		{name: "invalid port", ports: "port invalid\n", fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, script := range map[string]string{
				"sshd": "#!/bin/sh\n[ \"$SSHD_FAILS\" != true ] || exit 1\nprintf '%s' \"$PORTS\"\n",
				"ufw": `#!/bin/sh
if [ "$1" = status ]; then
 printf '%s\n' 'Status: active' 'Default: deny (incoming), allow (outgoing)'
 while read -r action port; do
  if [ "$action" = allow ]; then printf '%s ALLOW IN Anywhere\n' "$port"; fi
 done < "$EVENTS"
else
 printf '%s\n' "$*" >> "$EVENTS"
fi
`,
			} {
				if err := runexec.WriteExecutable(filepath.Join(dir, name), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			events := filepath.Join(dir, "events")
			if err := os.WriteFile(events, nil, 0600); err != nil {
				t.Fatal(err)
			}
			env := []string{"PATH=" + dir + ":" + os.Getenv("PATH"), "PORTS=" + tc.ports, "connection_port=" + tc.connected, "EVENTS=" + events, "SSHD_FAILS=" + map[bool]string{true: "true", false: "false"}[tc.sshdFails]}
			var firewall bootstrapStep
			for _, step := range bootstrapSteps("todo", "key") {
				if step.name == "firewall" {
					firewall = step
				}
			}
			run := func(script string) error {
				_, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "sh", Args: []string{"-ec", strings.ReplaceAll(script, "/usr/sbin/sshd", filepath.Join(dir, "sshd"))}, Env: env})
				return err
			}
			if err := run(firewall.check); err == nil {
				t.Fatal("probe accepted missing rules")
			}
			if err := run(firewall.apply); (err != nil) != tc.fails {
				t.Fatalf("apply error = %v, want failure %v", err, tc.fails)
			}
			data, err := os.ReadFile(events)
			if err != nil {
				t.Fatal(err)
			}
			if tc.fails {
				if len(data) != 0 {
					t.Fatalf("unsafe discovery changed firewall: %s", data)
				}
				return
			}
			for _, port := range strings.Fields(tc.want) {
				allow := "allow " + port + "/tcp\n"
				if i := strings.Index(string(data), allow); i < 0 || i > strings.Index(string(data), "--force enable") {
					t.Fatalf("port %s not allowed before enable: %s", port, data)
				}
				// Keep the standard rules but remove each required SSH rule in turn.
				without := strings.ReplaceAll(string(data), allow, "")
				if err := os.WriteFile(events, []byte(without), 0600); err != nil {
					t.Fatal(err)
				}
				if err := run(firewall.check); err == nil {
					t.Fatalf("probe accepted missing SSH port %s", port)
				}
			}
			if err := os.WriteFile(events, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := run(firewall.check); err != nil {
				t.Fatalf("probe rejected complete rules: %v", err)
			}
		})
	}
}
