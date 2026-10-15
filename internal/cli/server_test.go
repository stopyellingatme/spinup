package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestServerCLI(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		args    []string
		code    int
		message string
	}{
		{"help", []string{"server", "--help"}, 0, "bootstrap"},
		{"bootstrap help", []string{"server", "bootstrap", "--help"}, 0, "--ssh-key"},
		{"audit help", []string{"server", "audit", "--help"}, 0, "--json"},
		{"logs help", []string{"server", "logs", "--help"}, 0, "--since"},
		{"missing service", []string{"server", "logs"}, ui.ExitUsage, "Try:"},
		{"ssh extra argument", []string{"server", "ssh", "command"}, ui.ExitUsage, "Try:"},
		{"bootstrap extra argument", []string{"server", "bootstrap", "host"}, ui.ExitUsage, "Try:"},
		{"conflicting mode", []string{"server", "bootstrap", "--plan", "--print-cloud-init"}, ui.ExitError, "cannot be combined"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			code, out, stderr := run(t, tt.args...)
			if code != tt.code || !strings.Contains(out+stderr, tt.message) {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
		})
	}
}

func TestServerCloudInitCLI(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deploy", "cloud-init"), 0755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"spinup.toml":                      "[app]\nname = 'todo'\n[env.staging]\n",
		"deploy/cloud-init/user-data.yaml": "#cloud-config\nusers:\n  - name: deploy\n    ssh_authorized_keys:\n      - SPINUP_DEPLOY_SSH_KEY\n",
		"operator.pub":                     "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA operator\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	code, out, stderr := run(t, "server", "bootstrap", "--env", "staging", "--dir", root, "--ssh-key", filepath.Join(root, "operator.pub"), "--print-cloud-init")
	if code != 0 || stderr != "" || !strings.Contains(out, "ssh-ed25519") || strings.Contains(out, "SPINUP_DEPLOY_SSH_KEY") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}
