package cli

import (
	"strings"
	"testing"
)

func TestInfraCommands(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"plan", "apply", "output", "destroy"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			code, out, errOut := run(t, "infra", action, "--help")
			if code != 0 || !strings.Contains(out, "--env") {
				t.Fatalf("help = %d, %s, %s", code, out, errOut)
			}
			if action == "destroy" && !strings.Contains(out, "--i-know") {
				t.Fatal("destroy has no --i-know flag")
			}
			code, _, errOut = run(t, "infra", action, "--non-interactive")
			if code == 0 || !strings.Contains(errOut, "Try:") || !strings.Contains(errOut, "--env") {
				t.Fatalf("missing environment = %d, %s", code, errOut)
			}
		})
	}
}
