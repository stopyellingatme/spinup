package cli

import (
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestMonitorCLI(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"monitor", "--help"}, 0, "dashboards"},
		{[]string{"monitor", "open", "--help"}, 0, "--no-browser"},
		{[]string{"monitor", "logs", "--help"}, 0, "--query"},
		{[]string{"monitor", "silence", "--help"}, 0, "--for"},
		{[]string{"monitor", "dashboards", "sync", "--help"}, 0, "--env"},
		{[]string{"monitor", "trace"}, ui.ExitUsage, "Try:"},
		{[]string{"monitor", "silence"}, ui.ExitUsage, "Try:"},
		{[]string{"monitor", "status", "extra"}, ui.ExitUsage, "Try:"},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()
			code, out, stderr := run(t, tt.args...)
			if code != tt.code || !strings.Contains(out+stderr, tt.want) {
				t.Fatalf("code=%d out=%s err=%s", code, out, stderr)
			}
		})
	}
}
