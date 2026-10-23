package provision

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

const (
	sshWaitTotal    = 5 * time.Minute
	sshBackoffStart = 2 * time.Second
	sshBackoffMax   = 15 * time.Second
	cloudInitLimit  = 20 * time.Minute
	outputLimit     = 4096
)

// runText runs argv on the VM. It returns stdout, and the text of stderr and the error for diagnosis.
func (r *run) runText(ctx context.Context, argv ...string) (stdout, diag string, err error) {
	res, err := r.runner.Run(ctx, remote.Cmd{Argv: argv, Timeout: 30 * time.Second})
	if err != nil {
		diag = string(res.Stderr) + " " + err.Error()
	}
	return string(res.Stdout), diag, err
}

// run runs argv on the VM after the first connection worked. A host key error is fatal at once.
func (r *run) run(ctx context.Context, argv ...string) (string, error) {
	out, diag, err := r.runText(ctx, argv...)
	if err != nil {
		if remote.IsHostKeyError(diag) {
			return "", hostKeyMismatch()
		}
		return "", failWrap("run a command on the droplet", err, "run the command again")
	}
	return out, nil
}

func hostKeyMismatch() error {
	return fail("verify the host key of the droplet", "the key does not match the pin in spinup.toml, so the droplet was rebuilt or a third party answers", "pass --recreate to make a new droplet with a new pin")
}

// dial opens the SSH connection with the current pins.
func (r *run) dial(ctx context.Context) error {
	if r.runner != nil {
		_ = r.runner.Close(ctx)
	}
	conn, err := r.d.Dial(ctx, r.envCfg)
	if err != nil {
		return failWrap("open the SSH connection", err, "check host, user, and host_keys of the environment in spinup.toml")
	}
	r.runner = conn
	return nil
}

// waitSSH waits for the first SSH command to work (ADR 0017 §7 step 8).
// A host key error is retried during the wait, because a pinned connection never trusts a wrong key.
func (r *run) waitSSH(ctx context.Context) error {
	if err := r.dial(ctx); err != nil {
		return err
	}
	start := r.d.Now()
	delay := sshBackoffStart
	var lastDiag string
	hostKey := false
	for {
		_, diag, err := r.runText(ctx, "true")
		if err == nil {
			r.connected = true
			return nil
		}
		lastDiag = diag
		hostKey = remote.IsHostKeyError(diag)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if r.d.Now().Sub(start) >= sshWaitTotal {
			break
		}
		if err := r.d.Sleep(ctx, delay); err != nil {
			return err
		}
		delay = min(delay*2, sshBackoffMax)
	}
	if hostKey {
		return hostKeyMismatch()
	}
	return fail("connect to the droplet", "SSH did not work after "+sshWaitTotal.String()+": "+tail(lastDiag), "check the firewall rule for port 22 (--ssh-from), then run the command again")
}

// tail returns the last part of s on one line.
func tail(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = "..." + s[len(s)-300:]
	}
	return s
}

// waitCloudInit waits for cloud-init to finish (ADR 0017 §7 step 9).
func (r *run) waitCloudInit(ctx context.Context) error {
	r.say("Waiting for cloud-init on the droplet.")
	var out strings.Builder
	res, err := r.runner.Run(ctx, remote.Cmd{
		Argv:    []string{"cloud-init", "status", "--wait", "--long"},
		Stdout:  &out,
		Stderr:  &out,
		Timeout: cloudInitLimit,
	})
	text := out.String()
	if text == "" {
		text = string(res.Stdout) + string(res.Stderr)
	}
	if remote.IsHostKeyError(text) {
		return hostKeyMismatch()
	}
	code := res.ExitCode
	var ee *runexec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		code = ee.Code
	default:
		if remote.IsHostKeyError(text + " " + err.Error()) {
			return hostKeyMismatch()
		}
		return failWrap("wait for cloud-init", err, "run the command again")
	}
	switch code {
	case 0:
		return nil
	case 2:
		r.warn("Warning: cloud-init finished with recoverable errors:\n%s", clip(text))
		return nil
	}
	return fail("run cloud-init on the droplet", fmt.Sprintf("it ended with exit code %d:\n%s", code, clip(text)), "spinup deploy digital-ocean --recreate")
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > outputLimit {
		s = "..." + s[len(s)-outputLimit:]
	}
	return s
}

// reconnect closes the SSH master and opens a new connection, so the docker group applies.
func (r *run) reconnect(ctx context.Context) error {
	if err := r.dial(ctx); err != nil {
		return err
	}
	_, err := r.run(ctx, "true")
	return err
}
