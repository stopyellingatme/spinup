package cli

import (
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestBackupCLI(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		args    []string
		code    int
		message string
	}{
		{"help", []string{"backup", "--help"}, 0, "status"},
		{"now help", []string{"backup", "now", "--help"}, 0, "--type"},
		{"bad type", []string{"backup", "now", "--type", "weekly", "--env", "staging"}, ui.ExitError, "full, diff, or incr"},
		{"no args", []string{"backup", "list", "extra"}, ui.ExitUsage, "Try:"},
		{"no project", []string{"backup", "status", "--env", "staging", "--dir", t.TempDir()}, ui.ExitError, "spinup.toml"},
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
