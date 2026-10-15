package cli

import (
	"strings"
	"testing"
)

func TestRollbackCLI(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"rollback", "--help"}, 0, "--with-db-plan"},
		{[]string{"rollback", "--to", "invalid"}, 1, "Try:"},
		{[]string{"rollback", "--dir", t.TempDir(), "--env", "staging"}, 1, "Cannot find the project"},
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
