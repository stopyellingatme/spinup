package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestSelfUpdateOffline(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"self-update", "--offline"}, {"self-update", "--check", "--offline"}, {"version", "--check", "--offline"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			var out, stderr bytes.Buffer
			code := Execute(context.Background(), args, strings.NewReader(""), &out, &stderr)
			if code != 2 || !strings.Contains(stderr.String(), "Try: remove --offline") {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
		})
	}
}

func TestSelfUpdateHelp(t *testing.T) {
	t.Parallel()
	var out, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"self-update", "--help"}, strings.NewReader(""), &out, &stderr)
	for _, flag := range []string{"--check", "--channel", "--version", "--require-signature"} {
		if code != 0 || !strings.Contains(out.String(), flag) {
			t.Fatalf("missing %s: %s %s", flag, out.String(), stderr.String())
		}
	}
}
