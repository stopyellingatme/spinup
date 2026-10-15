package devloop

// This file is the second permitted os/exec user, after internal/exec.
// internal/exec streams output but only offers immediate SIGKILL on cancellation.
// Dev processes require group SIGTERM followed by a five-second grace period.
// Air uses four seconds internally to stop its separate application group first.

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"time"

	"github.com/stopyellingatme/spinup/internal/exec"
)

// Child is a process group started by a Starter. Wait must be called once.
type Child interface {
	// Wait reaps the group leader and drains its output.
	Wait() error
	// Terminate sends SIGTERM to the process group.
	Terminate() error
	// Kill sends SIGKILL to the process group.
	Kill() error
	// Alive reports whether the process group still exists.
	Alive() bool
}

// Starter starts a child in a new process group with streaming output.
// Cancellation and reaping belong to Supervisor, including during readiness.
type Starter interface {
	// Start creates a separate process group and attaches output writers.
	Start(context.Context, exec.Cmd) (Child, error)
}

// OSStarter implements the graceful dev process lifecycle.
type OSStarter struct{}

// Start starts c in its own process group. It never inherits terminal input.
func (OSStarter) Start(ctx context.Context, c exec.Cmd) (Child, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := c.Name
	if filepath.Base(path) == path {
		var err error
		path, err = exec.LookPath(path)
		if err != nil {
			return nil, err
		}
	}
	command := osexec.CommandContext(context.WithoutCancel(ctx), path, c.Args...)
	// Supervisor owns cancellation. WaitDelay must only start after process exit.
	command.Dir = c.Dir
	command.Env = append(os.Environ(), c.Env...)
	command.Stdout, command.Stderr = c.Stdout, c.Stderr
	command.Stdin = c.Stdin
	command.SysProcAttr = processAttributes()
	command.WaitDelay = 2 * time.Second
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w\nTry: check the executable and working directory", c.String(), err)
	}
	return &osChild{command}, nil
}

type osChild struct{ command *osexec.Cmd }

func (c *osChild) Wait() error      { return c.command.Wait() }
func (c *osChild) Terminate() error { return signalGroup(c.command.Process.Pid, false) }
func (c *osChild) Kill() error      { return signalGroup(c.command.Process.Pid, true) }
func (c *osChild) Alive() bool      { return groupAlive(c.command.Process.Pid) }
