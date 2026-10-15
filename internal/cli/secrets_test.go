package cli

import (
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestSecretsCLI(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		args    []string
		code    int
		message string
	}{
		{"help", []string{"secrets", "--help"}, 0, "rekey"},
		{"init help", []string{"secrets", "init", "--help"}, 0, "--recovery-recipient"},
		{"stdin only", []string{"secrets", "set", "NAME", "secret-value"}, ui.ExitUsage, "Try:"},
		{"name required", []string{"secrets", "get"}, ui.ExitUsage, "Try:"},
		{"env required", []string{"secrets", "init"}, ui.ExitError, "--env"},
		{"edit terminal", []string{"secrets", "edit", "--env", "staging"}, ui.ExitError, "terminal"},
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
