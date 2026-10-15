package cli

import (
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestDeployCLI(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, tt := range []struct {
		name    string
		args    []string
		code    int
		message string
	}{
		{"help", []string{"deploy", "--help"}, ui.ExitOK, "--no-auto-rollback"},
		{"help lists subcommands", []string{"deploy", "--help"}, ui.ExitOK, "unlock"},
		{"health timeout flag", []string{"deploy", "--help"}, ui.ExitOK, "--health-timeout duration"},
		{"status help", []string{"deploy", "status", "--help"}, ui.ExitOK, "--accept-new-host-key"},
		{"plan and resume", []string{"deploy", "--plan", "--resume", "--dir", dir}, ui.ExitUsage, "Try:"},
		{"extra argument", []string{"deploy", "staging"}, ui.ExitUsage, "Try:"},
		{"no project", []string{"deploy", "--env", "staging", "--dir", dir}, ui.ExitError, "spinup.toml was not found"},
		{"plan without project", []string{"deploy", "--plan", "--dir", dir}, ui.ExitError, "Try: run spinup deploy inside a spinup project"},
		{"status without project", []string{"deploy", "status", "--dir", dir}, ui.ExitError, "Cannot find the project"},
		{"unlock needs force", []string{"deploy", "unlock", "--dir", dir}, ui.ExitError, "unlock --force"},
		{"unlock without project", []string{"deploy", "unlock", "--force", "--dir", dir}, ui.ExitError, "Cannot find the project"},
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

func TestProjectRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if got := projectRoot(dir); got != dir {
		t.Fatalf("projectRoot outside a project = %q", got)
	}
}
