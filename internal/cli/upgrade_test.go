package cli

import (
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestUpgradeFlags(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"--plan", "--continue"}, {"--plan", "--abort"}, {"--continue", "--abort"}, {"extra"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			code, _, stderr := run(t, append([]string{"upgrade"}, args...)...)
			if code != ui.ExitUsage || !strings.Contains(stderr, "Try:") {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
		})
	}
}

func TestUpgradeHelp(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := run(t, "upgrade", "--help")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	for _, flag := range []string{"--to", "--plan", "--continue", "--abort", "--allow-dirty", "--force-managed", "--no-verify", "--commit-conflicts", "--yes", "--offline"} {
		if !strings.Contains(stdout, flag) {
			t.Errorf("help missing %s", flag)
		}
	}
}
