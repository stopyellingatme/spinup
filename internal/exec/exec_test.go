package exec

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOSRunnerRedactsOutputErrorsAndCommand(t *testing.T) {
	t.Parallel()

	const secret = "very-secret-value"
	var stream bytes.Buffer
	result, err := (OSRunner{}).Run(context.Background(), Cmd{
		Name:   "sh",
		Args:   []string{"-c", "printf '%s' \"$TOKEN\"; printf '%s\\n' \"$TOKEN\" >&2; exit 7"},
		Env:    []string{"TOKEN=" + secret},
		Redact: []string{secret},
		Stdout: &stream,
		Stderr: &stream,
	})
	if err == nil {
		t.Fatal("Run() error = nil")
	}
	if strings.Contains(string(result.Stdout), secret) || strings.Contains(string(result.Stderr), secret) {
		t.Fatalf("result exposes secret: %#v", result)
	}
	if strings.Contains(stream.String(), secret) {
		t.Fatalf("stream exposes secret: %q", stream.String())
	}
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Run() error = %T, want *ExitError", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(exitErr.Cmd, secret) {
		t.Fatalf("error exposes secret: %v", err)
	}
	if got, want := exitErr.Code, 7; got != want {
		t.Fatalf("ExitError.Code = %d, want %d", got, want)
	}

	command := Cmd{Name: "tool", Args: []string{"a value", secret}, Redact: []string{secret}}
	if got := command.String(); strings.Contains(got, secret) || !strings.Contains(got, "***") {
		t.Fatalf("Cmd.String() = %q, want redacted secret", got)
	}
}

func TestOSRunnerTimeoutKillsProcessGroup(t *testing.T) {
	if isWindows() {
		t.Skip("Unix process groups are not available on Windows")
	}
	t.Parallel()

	started := time.Now()
	_, err := (OSRunner{}).Run(context.Background(), Cmd{
		Name:    "sh",
		Args:    []string{"-c", "sleep 30 & wait"},
		Timeout: 100 * time.Millisecond,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("Run() took %s, child process was not killed", elapsed)
	}
}

func TestOSRunnerContextCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err := (OSRunner{}).Run(ctx, Cmd{Name: "sh", Args: []string{"-c", "sleep 30"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context canceled", err)
	}
}

func TestOSRunnerExitErrorHasLastTwentyStderrLines(t *testing.T) {
	t.Parallel()

	_, err := (OSRunner{}).Run(context.Background(), Cmd{
		Name: "sh",
		Args: []string{"-c", "i=1; while [ $i -le 21 ]; do echo line$i >&2; i=$((i + 1)); done; exit 1"},
	})
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Run() error = %T, want *ExitError", err)
	}
	if strings.Contains(exitErr.StderrTail, "line1\n") || !strings.HasPrefix(exitErr.StderrTail, "line2\n") || !strings.HasSuffix(exitErr.StderrTail, "line21") {
		t.Fatalf("StderrTail = %q, want lines 2 through 21", exitErr.StderrTail)
	}
}

func TestLookPathMissingBinary(t *testing.T) {
	t.Parallel()

	_, err := LookPath("spinup-definitely-missing-binary")
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("LookPath() error = %T, want *NotFoundError", err)
	}
	if got, want := notFound.Name, "spinup-definitely-missing-binary"; got != want {
		t.Fatalf("NotFoundError.Name = %q, want %q", got, want)
	}
	if !strings.HasSuffix(err.Error(), "Try: install spinup-definitely-missing-binary, or run spinup doctor") {
		t.Fatalf("NotFoundError = %q", err)
	}
}

func TestRunJSON(t *testing.T) {
	t.Parallel()

	var value struct {
		Name string `json:"name"`
	}
	err := RunJSON(context.Background(), OSRunner{}, Cmd{
		Name: "sh",
		Args: []string{"-c", "printf '{\"name\":\"spinup\"}'"},
	}, &value)
	if err != nil {
		t.Fatalf("RunJSON() error = %v", err)
	}
	if got, want := value.Name, "spinup"; got != want {
		t.Fatalf("RunJSON() decoded %q, want %q", got, want)
	}
}

func TestRedactingSinkRedactsValuesAcrossWrites(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	sink := newRedactingSink(&output, []string{"secret"}, &sync.Mutex{})
	if _, err := sink.Write([]byte("before sec")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if strings.Contains(output.String(), "sec") {
		t.Fatalf("sink leaked partial secret: %q", output.String())
	}
	if _, err := sink.Write([]byte("ret after")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	sink.Flush()
	if got, want := output.String(), "before *** after"; got != want {
		t.Fatalf("sink output = %q, want %q", got, want)
	}
}

func TestFakeOrderingUnexpectedCallsAndAssertDone(t *testing.T) {
	t.Parallel()

	var fake Fake
	fake.Expect("first", "arg").Return("one", "", 0)
	fake.Expect("second").Return("", "bad", 5)

	if _, err := fake.Run(context.Background(), Cmd{Name: "second"}); err == nil || !strings.Contains(err.Error(), "second") {
		t.Fatalf("unexpected call error = %v", err)
	}
	result, err := fake.Run(context.Background(), Cmd{Name: "first", Args: []string{"arg", "extra"}})
	if err != nil || string(result.Stdout) != "one" {
		t.Fatalf("first call = (%q, %v)", result.Stdout, err)
	}
	result, err = fake.Run(context.Background(), Cmd{Name: "second"})
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || result.ExitCode != 5 {
		t.Fatalf("second call = (%+v, %v), want exit code error", result, err)
	}
	fake.AssertDone(t)

	calls := fake.Calls()
	if got, want := len(calls), 3; got != want {
		t.Fatalf("Calls() returned %d calls, want %d", got, want)
	}
}

func TestFakeFail(t *testing.T) {
	t.Parallel()

	want := errors.New("scripted failure")
	var fake Fake
	fake.Expect("tool").Fail(want)
	_, err := fake.Run(context.Background(), Cmd{Name: "tool"})
	if !errors.Is(err, want) {
		t.Fatalf("Run() error = %v, want %v", err, want)
	}
	fake.AssertDone(t)
}

func isWindows() bool { return runtime.GOOS == "windows" }
