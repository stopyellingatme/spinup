package cli

import (
	"strings"
	"testing"
)

func TestBackupM7CLI(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		args []string
		want string
		fail bool
	}{
		{"verify help", []string{"backup", "verify", "--help"}, "--label", false},
		{"restore help", []string{"backup", "restore", "--help"}, "--i-know", false},
		{"prune help", []string{"backup", "prune", "--help"}, "--plan", false},
		{"invalid time", []string{"backup", "verify", "--time", "yesterday"}, "RFC 3339", true},
		{"exclusive targets", []string{"backup", "verify", "--label", "a", "--time", "2026-10-15T00:00:00Z"}, "--label and --time", true},
		{"required destination", []string{"backup", "restore"}, "--into", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			code, out, stderr := run(t, tt.args...)
			if (code != 0) != tt.fail || !strings.Contains(out+stderr, tt.want) {
				t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
			}
		})
	}
}
