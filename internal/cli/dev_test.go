package cli

import (
	"context"
	"os"
	"reflect"
	"strings"
	"syscall"
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

func TestDevContextHandlesSIGHUP(t *testing.T) {
	original := notifyContext
	t.Cleanup(func() { notifyContext = original })
	var got []os.Signal
	notifyContext = func(parent context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
		got = signals
		return context.WithCancel(parent)
	}
	_, stop := devContext(context.Background())
	stop()
	if !reflect.DeepEqual(got, []os.Signal{syscall.SIGHUP}) {
		t.Fatalf("signals = %v, want SIGHUP", got)
	}
}
