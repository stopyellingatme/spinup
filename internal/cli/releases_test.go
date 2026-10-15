package cli

import (
	"strings"
	"testing"
)

func TestReleasesCLI(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"releases", "--help"}, 0, "prune"},
		{[]string{"releases", "show", "invalid"}, 1, "Try:"},
		{[]string{"releases", "prune", "--keep", "0"}, 1, "positive number"},
		{[]string{"releases", "list", "--dir", t.TempDir()}, 1, "Cannot find the project"},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()
			code, out, stderr := run(t, tt.args...)
			if code != tt.code || !strings.Contains(out+stderr, tt.want) {
				t.Fatal(code, out, stderr)
			}
		})
	}
}
