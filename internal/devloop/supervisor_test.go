//go:build !windows

package devloop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/exec"
)

func TestSupervisorPrefixes(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := (Supervisor{Out: &out, NoColor: true}).Run(context.Background(), []Process{{Name: "api", Cmd: exec.Cmd{Name: "sh", Args: []string{"-c", "printf 'first\\npartial'; printf 'error\\n' >&2"}}}})
	if err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("unexpected exit: %v", err)
	}
	for _, line := range []string{"api  | first\n", "api  | partial\n", "api  | error\n"} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("missing %q in %q", line, out.String())
		}
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Fatal("unexpected color")
	}
}

func TestSupervisorRestartBackoff(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	starter := &recordingStarter{base: OSStarter{}, cancel: cancel, stopAfter: 3}
	var out bytes.Buffer
	err := (Supervisor{Starter: starter, Out: &out, NoColor: true, Backoff: 40 * time.Millisecond}).Run(ctx, []Process{{Name: "api", Cmd: exec.Cmd{Name: "sh", Args: []string{"-c", "exit 3"}}, Restart: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(starter.times) != 3 {
		t.Fatalf("starts: %d", len(starter.times))
	}
	if starter.times[1].Sub(starter.times[0]) < 40*time.Millisecond || starter.times[2].Sub(starter.times[1]) < 80*time.Millisecond {
		t.Fatalf("restart delays: %v", starter.times)
	}
	if !strings.Contains(out.String(), "Restarting in 40ms") || !strings.Contains(out.String(), "Restarting in 80ms") {
		t.Fatalf("logs: %s", &out)
	}
}

type recordingStarter struct {
	base      Starter
	times     []time.Time
	cancel    context.CancelFunc
	stopAfter int
}

func (s *recordingStarter) Start(ctx context.Context, c exec.Cmd) (Child, error) {
	s.times = append(s.times, time.Now())
	child, err := s.base.Start(ctx, c)
	if len(s.times) == s.stopAfter {
		s.cancel()
	}
	return child, err
}

func waitFile(ctx context.Context, path string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func TestSupervisorStopsGroup(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- (Supervisor{NoColor: true}).Run(ctx, []Process{{Name: "api", Cmd: exec.Cmd{Name: "sh", Dir: directory, Args: []string{"-c", `trap 'wait; exit 0' TERM; sleep 60 & echo $! > child.pid; wait`}}, Ready: func(ctx context.Context) error { return waitFile(ctx, path) }}})
	}()
	if err := waitFile(ctx, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("supervisor did not stop")
	}
	deadline := time.Now().Add(time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("orphan %d: %v", pid, err)
	}
}

func TestSupervisorEscalatesAfterGrace(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "ready")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- (Supervisor{}).Run(ctx, []Process{{Name: "api", Cmd: exec.Cmd{Name: "sh", Dir: directory, Args: []string{"-c", `trap '' TERM; echo ready > ready; exec sleep 60`}}}})
	}()
	readyCtx, readyCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readyCancel()
	if err := waitFile(readyCtx, path); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("SIGKILL did not reap the child")
	}
	if elapsed := time.Since(start); elapsed < 5*time.Second {
		t.Fatalf("grace period too short: %s", elapsed)
	}
}

func TestSupervisorReadinessFailure(t *testing.T) {
	t.Parallel()
	err := (Supervisor{}).Run(context.Background(), []Process{{Name: "api", Cmd: exec.Cmd{Name: "sh", Args: []string{"-c", "sleep 60"}}, Ready: func(context.Context) error { return fmt.Errorf("not ready") }}})
	if err == nil || !strings.Contains(err.Error(), "readiness failed") {
		t.Fatalf("got %v", err)
	}
}

func TestLogColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var out bytes.Buffer
	_ = (Supervisor{Out: &out}).Run(context.Background(), []Process{{Name: "api", Cmd: exec.Cmd{Name: "sh", Args: []string{"-c", "echo hi"}}}})
	if strings.Contains(out.String(), "\x1b") {
		t.Fatal("NO_COLOR ignored")
	}
	out.Reset()
	log := &logMux{out: &out, width: 4, color: true}
	writer := &lineWriter{log: log, name: "api"}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { _, _ = writer.Write([]byte("hello\n")) })
	}
	wg.Wait()
	if strings.Count(out.String(), "\x1b[31mapi  |\x1b[0m hello\n") != 2 {
		t.Fatalf("colored output: %q", out.String())
	}
}

func TestSupervisorRechecksReadiness(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	count := 0
	err := (Supervisor{Backoff: time.Millisecond}).Run(ctx, []Process{{Name: "api", Cmd: exec.Cmd{Name: "sh", Args: []string{"-c", "sleep 0.03; exit 1"}}, Restart: true, Ready: func(context.Context) error {
		count++
		if count == 2 {
			cancel()
		}
		return nil
	}}})
	if err != nil || count != 2 {
		t.Fatalf("readiness count=%d err=%v", count, err)
	}
}

func TestSupervisorReadinessDeadlineFails(t *testing.T) {
	t.Parallel()
	err := (Supervisor{}).Run(context.Background(), []Process{{Name: "api", Cmd: exec.Cmd{Name: "sh", Args: []string{"-c", "sleep 60"}}, Ready: func(context.Context) error { return context.DeadlineExceeded }}})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("readiness error: %v", err)
	}
}

func TestSupervisorGraceForDescendant(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := `trap 'exit 0' TERM; "$SPINUP_DEV_TEST_BINARY" -test.run '^TestDevGraceHelper$' & wait`
	go func() {
		done <- (Supervisor{}).Run(ctx, []Process{{Name: "api", Cmd: exec.Cmd{Name: "sh", Dir: directory, Args: []string{"-c", command}, Env: []string{"SPINUP_DEV_TEST_BINARY=" + binary, "SPINUP_DEV_GRACE_HELPER=1"}}}})
	}()
	readyCtx, readyCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readyCancel()
	if err := waitFile(readyCtx, filepath.Join(directory, "ready")); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("group did not stop")
	}
	if _, err := os.Stat(filepath.Join(directory, "finished")); err != nil {
		t.Fatalf("descendant lost its grace period: %v", err)
	}
}

func TestSupervisorRelativeExecutable(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "helper"), []byte("#!/bin/sh\necho relative\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := (Supervisor{Out: &out, NoColor: true}).Run(context.Background(), []Process{{Name: "api", Dir: directory, Cmd: exec.Cmd{Name: "./helper"}}})
	if err == nil || !strings.Contains(out.String(), "api  | relative\n") {
		t.Fatalf("relative executable: %v, %q", err, out.String())
	}
}

// TestDevGraceHelper acts as a descendant with a registered signal handler.
func TestDevGraceHelper(t *testing.T) {
	if os.Getenv("SPINUP_DEV_GRACE_HELPER") != "1" {
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	if err := os.WriteFile("ready", []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	<-signals
	time.Sleep(200 * time.Millisecond)
	if err := os.WriteFile("finished", []byte("finished"), 0600); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}
