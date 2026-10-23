package cli

import (
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestDeployDigitalOceanCLI(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, tt := range []struct {
		name    string
		args    []string
		code    int
		message string
	}{
		{"help", []string{"deploy", "digital-ocean", "--help"}, ui.ExitOK, "--recovery-recipient"},
		{"alias do", []string{"deploy", "do", "--help"}, ui.ExitOK, "--dns-timeout duration"},
		{"alias digitalocean", []string{"deploy", "digitalocean", "--help"}, ui.ExitOK, "--i-know"},
		{"pass-through flags", []string{"deploy", "do", "--help"}, ui.ExitOK, "--no-auto-rollback"},
		{"help lists subcommands", []string{"deploy", "do", "--help"}, ui.ExitOK, "logout"},
		{"login help", []string{"deploy", "do", "login", "--help"}, ui.ExitOK, "--no-browser"},
		{"offline", []string{"deploy", "do", "--offline", "--dir", dir}, ui.ExitUsage, "Try: run the command without --offline"},
		{"offline status", []string{"deploy", "do", "status", "--offline"}, ui.ExitUsage, "Try: run the command without --offline"},
		{"destroy needs i-know", []string{"deploy", "do", "destroy", "--dir", dir}, ui.ExitError, "pass --i-know"},
		{"extra argument", []string{"deploy", "do", "staging"}, ui.ExitUsage, "Try:"},
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

func TestDeployKeepsItsFlags(t *testing.T) {
	t.Parallel()
	code, out, stderr := run(t, "deploy", "--help")
	if code != ui.ExitOK {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	for _, flag := range []string{"--allow-dirty", "--allow-destructive", "--skip-build", "--skip-backup", "--tag", "--platform", "--health-timeout", "--no-auto-rollback", "--redeploy", "--resume", "--plan"} {
		if !strings.Contains(out, flag) {
			t.Errorf("spinup deploy lost the flag %s", flag)
		}
	}
}
