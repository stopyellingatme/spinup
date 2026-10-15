//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigureProcessInteractive(t *testing.T) {
	t.Parallel()

	for _, interactive := range []bool{false, true} {
		t.Run(fmt.Sprintf("interactive-%t", interactive), func(t *testing.T) {
			t.Parallel()
			var command osexec.Cmd
			configureProcess(&command, interactive)
			if command.SysProcAttr == nil || command.SysProcAttr.Setpgid != !interactive {
				t.Fatalf("SysProcAttr = %+v, want Setpgid = %t", command.SysProcAttr, !interactive)
			}
		})
	}
}

func TestOSRunnerKillsGrandchildAfterParentExit(t *testing.T) {
	t.Parallel()

	result, err := (OSRunner{}).Run(context.Background(), Cmd{
		Name:    "sh",
		Args:    []string{"-c", "(sleep 3; echo survived) & echo hi"},
		// Long enough for "echo hi" on a loaded machine, and well before the grandchild wakes.
		Timeout: time.Second,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want deadline exceeded", err)
	}
	if string(result.Stdout) != "hi\n" {
		t.Fatalf("Run() stdout = %q, grandchild survived cancellation", result.Stdout)
	}
}

func TestOSRunnerBoundsWaitForGrandchildOutput(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			command := Cmd{Name: "sh", Args: []string{"-c", "sleep 8 & echo hi"}}
			want := context.DeadlineExceeded
			if mode == "timeout" {
				command.Timeout = time.Second
			} else {
				want = context.Canceled
				timer := time.AfterFunc(time.Second, cancel)
				defer timer.Stop()
			}
			started := time.Now()
			result, err := (OSRunner{}).Run(ctx, command)
			if !errors.Is(err, want) {
				t.Errorf("Run() error = %v, want %v", err, want)
			}
			if elapsed := time.Since(started); elapsed > time.Second+processWaitDelay+time.Second {
				t.Errorf("Run() took %s, want at most timeout + WaitDelay + 1s", elapsed)
			}
			if string(result.Stdout) != "hi\n" {
				t.Errorf("Run() stdout = %q, want hi", result.Stdout)
			}
		})
	}
}

func TestOSRunnerResolvesCommandDirectoryAndPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cmdName string
		path    string
		missing bool
	}{
		{"relative executable", "./bin/spinup-exec-test", "", false},
		{"PATH override", "spinup-exec-test", "absolute", false},
		{"relative PATH override", "spinup-exec-test", "relative", false},
		{"empty PATH override", "sh", "empty", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "spinup-exec-test"), []byte("#!/bin/sh\nprintf '%s' \"$VALUE\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			command := Cmd{Name: test.cmdName, Dir: dir, Env: []string{"VALUE=from-command-env"}}
			switch test.path {
			case "absolute":
				command.Env = append(command.Env, "PATH=", "PATH="+bin)
			case "relative":
				command.Env = append(command.Env, "PATH=bin")
			case "empty":
				command.Env = append(command.Env, "PATH=")
			}
			result, err := (OSRunner{}).Run(context.Background(), command)
			if test.missing {
				var notFound *NotFoundError
				if !errors.As(err, &notFound) {
					t.Fatalf("Run() error = %v, want *NotFoundError", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(result.Stdout) != "from-command-env" {
				t.Fatalf("Run() stdout = %q", result.Stdout)
			}
		})
	}
}
