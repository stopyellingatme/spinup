package monitor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// tunnelRunner preserves remote.New's SSH configuration and replaces the remote command.
type tunnelRunner struct {
	runner runexec.Runner
	port   int
	cancel bool
}

func (r tunnelRunner) Run(ctx context.Context, c runexec.Cmd) (runexec.Result, error) {
	// remote.Run appends exactly one quoted command after the destination.
	c.Args = append([]string(nil), c.Args[:len(c.Args)-1]...)
	index := len(c.Args) - 2 // the -- delimiter before the destination
	forward := "127.0.0.1:" + strconv.Itoa(r.port) + ":127.0.0.1:3000"
	flags := []string{"-N", "-o", "ExitOnForwardFailure=yes", "-L", forward}
	if r.cancel {
		flags = []string{"-O", "cancel", "-L", forward}
	}
	c.Args = append(append(append([]string(nil), c.Args[:index]...), flags...), c.Args[index:]...)
	return r.runner.Run(ctx, c)
}

func open(ctx context.Context, o Options, h remote.Host, runner runexec.Runner) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return failure("reserve tunnel port", err)
	}
	address := listener.Addr().String()
	_, portString, err := net.SplitHostPort(address)
	if err != nil {
		_ = listener.Close()
		return failure("read tunnel port", err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		_ = listener.Close()
		return failure("read tunnel port", err)
	}
	if err = listener.Close(); err != nil {
		return failure("release tunnel port", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	vm := remote.New(h, tunnelRunner{runner: runner, port: port}, remote.Options{AcceptNewHostKey: o.AcceptNewHostKey})
	// Cancel this forward without closing a master connection used by another command.
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		cleanup := remote.New(h, tunnelRunner{runner: runner, port: port, cancel: true}, remote.Options{AcceptNewHostKey: o.AcceptNewHostKey})
		_, _ = cleanup.Run(cleanupCtx, remote.Cmd{Argv: []string{"true"}})
	}()
	done := make(chan error, 1)
	go func() { _, e := vm.Run(ctx, remote.Cmd{Argv: []string{"true"}, Stderr: o.ErrOut}); done <- e }()
	// Always reap SSH after cancellation or a browser failure.
	exited := false
	defer func() {
		cancel()
		if !exited {
			<-done
		}
	}()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()
	ready := false
	for !ready {
		select {
		case err = <-done:
			exited = true
			if err == nil {
				err = errors.New("SSH exited before the tunnel was ready")
			}
			return failure("open Grafana tunnel", err)
		case <-ctx.Done():
			return nil
		case <-timeout.C:
			return failure("open Grafana tunnel", errors.New("SSH did not create the local listener"))
		case <-ticker.C:
			conn, e := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "tcp", address)
			if e == nil {
				_ = conn.Close()
				ready = true
			}
		}
	}
	url := "http://" + address
	fmt.Fprintf(o.Out, "%s\nPress Ctrl-C to close the tunnel.\n", url)
	if !o.NoBrowser {
		name := "xdg-open"
		if runtime.GOOS == "darwin" {
			name = "open"
		}
		if _, err = runner.Run(ctx, runexec.Cmd{Name: name, Args: []string{url}, Timeout: 10 * time.Second}); err != nil {
			fmt.Fprintln(o.ErrOut, "Cannot open the browser. Open the printed URL manually.")
		}
	}
	select {
	case <-ctx.Done():
		return nil
	case err = <-done:
		exited = true
		if err != nil && ctx.Err() == nil {
			return failure("run Grafana tunnel", err)
		}
		return nil
	}
}
