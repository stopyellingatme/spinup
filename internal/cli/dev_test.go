package cli

import (
	"strings"
	"testing"
)

func TestDevHelp(t *testing.T) {
	t.Parallel()
	code, out, errOut := run(t, "dev", "--help")
	if code != 0 {
		t.Fatal(errOut)
	}
	for _, flag := range []string{"--no-web", "--no-infra", "--lgtm", "--port", "--api-port", "--watch", "--down"} {
		if !strings.Contains(out, flag) {
			t.Errorf("missing flag %s", flag)
		}
	}
}
func TestDevRejectsInvalidOptions(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"dev", "--watch", "invalid"}, {"dev", "--port", "65536"}, {"dev", "--api-port", "-1"}} {
		code, _, errOut := run(t, args...)
		if code == 0 || !strings.Contains(errOut, "Try:") {
			t.Fatalf("%v: %d %s", args, code, errOut)
		}
	}
}
