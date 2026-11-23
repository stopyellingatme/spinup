package remote

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

// forwardStartup bounds the acquisition of one forward, including port collision retries.
// Tests shorten it.
var forwardStartup = 10 * time.Second

// forwardPoll is the interval of the local listener probe.
const forwardPoll = 25 * time.Millisecond

// forwardRunner preserves New's SSH configuration and replaces the remote command with a forward.
type forwardRunner struct {
	runner        runexec.Runner
	local, remote int
}

func (r forwardRunner) Run(ctx context.Context, c runexec.Cmd) (runexec.Result, error) {
	// sshRunner.run appends exactly one quoted command after the destination.
	c.Args = append([]string(nil), c.Args[:len(c.Args)-1]...)
	index := len(c.Args) - 2 // the -- delimiter before the destination
	forward := "127.0.0.1:" + strconv.Itoa(r.local) + ":127.0.0.1:" + strconv.Itoa(r.remote)
	flags := []string{"-N", "-o", "ExitOnForwardFailure=yes", "-L", forward}
	c.Args = append(append(append([]string(nil), c.Args[:index]...), flags...), c.Args[index:]...)
	return r.runner.Run(ctx, c)
}

// errPortCollision marks an SSH exit caused by a local port that another process took.
var errPortCollision = errors.New("the local port is in use")

type forwardOutcome struct {
	result runexec.Result
	err    error
}

// ForwardLoopback forwards a VM loopback port to an ephemeral local loopback port.
// The returned function closes only the forward owned by this invocation.
//
// The forward uses its own SSH connection. It never starts, uses, or stops a shared control
// master, and it keeps the host key pin and first-use policy of New. ForwardLoopback returns
// when the local listener of SSH accepts connections. The caller must still check the
// readiness endpoint of the forwarded service. A local port collision gets a new port until
// the startup bound expires. A failed acquisition stops its SSH process before it returns.
// ctx bounds the life of the forward. The close function is idempotent. It returns the SSH
// failure when SSH exited before the close, and nil otherwise.
func ForwardLoopback(ctx context.Context, h Host, r runexec.Runner, opts Options, port int) (localPort int, closeForward func() error, err error) {
	if port < 1 || port > 65535 {
		return 0, nil, fmt.Errorf("cannot forward VM port %d: the port is out of range\nTry: use a port from 1 through 65535", port)
	}
	opts.DisableMultiplexing = true
	deadline := time.Now().Add(forwardStartup)
	for {
		local, err := reserveLoopbackPort(ctx)
		if err != nil {
			return 0, nil, fmt.Errorf("cannot forward VM port %d: no local loopback port is free: %w\nTry: close unused local listeners and retry", port, err)
		}
		closeForward, err := startForward(ctx, h, r, opts, local, port, deadline)
		if err == nil {
			return local, closeForward, nil
		}
		if !errors.Is(err, errPortCollision) || !time.Now().Before(deadline) || ctx.Err() != nil {
			return 0, nil, err
		}
	}
}

// reserveLoopbackPort asks the kernel for a free IPv4 loopback port and releases it for SSH.
func reserveLoopbackPort(ctx context.Context) (int, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err = listener.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

func startForward(parent context.Context, h Host, r runexec.Runner, opts Options, local, port int, deadline time.Time) (func() error, error) {
	ctx, cancel := context.WithCancel(parent)
	vm := New(h, forwardRunner{runner: r, local: local, remote: port}, opts)
	done := make(chan forwardOutcome, 1)
	go func() {
		res, err := vm.Run(ctx, Cmd{Argv: []string{"true"}})
		done <- forwardOutcome{res, err}
	}()
	address := "127.0.0.1:" + strconv.Itoa(local)
	ticker := time.NewTicker(forwardPoll)
	defer ticker.Stop()
	timeout := time.NewTimer(time.Until(deadline))
	defer timeout.Stop()
	for {
		select {
		case o := <-done:
			cancel()
			if parent.Err() != nil {
				return nil, fmt.Errorf("cannot forward VM port %d: %w\nTry: retry the command", port, parent.Err())
			}
			return nil, forwardFailure(port, local, o, errors.New("SSH exited before the forward was ready"))
		case <-timeout.C:
			cancel()
			o := <-done
			return nil, forwardFailure(port, local, forwardOutcome{result: o.result}, fmt.Errorf("SSH did not open the forward within %s", forwardStartup))
		case <-ticker.C:
			if !listening(ctx, address) {
				continue
			}
			select {
			case o := <-done:
				// A foreign listener answered while SSH failed to bind the port.
				cancel()
				return nil, forwardFailure(port, local, o, errors.New("SSH exited before the forward was ready"))
			default:
			}
			return closer(ctx, port, local, cancel, done), nil
		}
	}
}

// closer returns the idempotent close function of one active forward.
func closer(ctx context.Context, port, local int, cancel context.CancelFunc, done <-chan forwardOutcome) func() error {
	var once sync.Once
	var err error
	return func() error {
		once.Do(func() {
			select {
			case o := <-done:
				// SSH exited before this close. A cancelled caller context is no failure.
				if ctx.Err() == nil {
					err = forwardFailure(port, local, o, errors.New("SSH exited while the forward was open"))
				}
				cancel()
			default:
				cancelled := ctx.Err() != nil
				cancel()
				// A runner error other than the cancellation means that SSH failed on its own.
				if o := <-done; !cancelled && o.err != nil && !errors.Is(o.err, context.Canceled) {
					err = forwardFailure(port, local, o, errors.New("SSH exited while the forward was open"))
				}
			}
		})
		return err
	}
}

// listening reports whether a local listener accepts connections at address.
func listening(ctx context.Context, address string) bool {
	dialer := net.Dialer{Timeout: 200 * time.Millisecond}
	conn, err := dialer.DialContext(ctx, "tcp4", address)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// forwardFailure builds the error of a failed SSH forward. It keeps the SSH diagnostics,
// including the host key messages of OpenSSH and their repair instructions.
func forwardFailure(port, local int, o forwardOutcome, fallback error) error {
	stderr := strings.TrimSpace(string(o.result.Stderr))
	if strings.Contains(stderr, "Address already in use") || strings.Contains(stderr, "cannot listen to port: "+strconv.Itoa(local)) {
		return fmt.Errorf("cannot forward VM port %d: local port %d: %w: %s\nTry: retry the command", port, local, errPortCollision, stderr)
	}
	cause := o.err
	var exit *runexec.ExitError
	if cause == nil || errors.As(cause, &exit) {
		// An exit error repeats the end of stderr. Use the whole stderr instead.
		cause = fallback
		if exit != nil {
			cause = fmt.Errorf("SSH exited with code %d", exit.Code)
		}
	}
	text := withoutTry(cause.Error())
	if stderr != "" {
		text += ": " + withoutTry(stderr)
	}
	if IsHostKeyError(stderr) {
		return fmt.Errorf("cannot forward VM port %d: SSH rejected the host key: %s\nTry: check host_keys in spinup.toml, or pass --accept-new-host-key only for a VM without a known key", port, text)
	}
	return fmt.Errorf("cannot forward VM port %d: %s\nTry: check the SSH connection with spinup doctor --remote", port, text)
}

// withoutTry removes an embedded Try line, so that the forward error has one.
func withoutTry(s string) string {
	if before, _, ok := strings.Cut(s, "\nTry:"); ok {
		return before
	}
	return s
}
