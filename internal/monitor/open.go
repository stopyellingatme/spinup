package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/browser"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

// tunnelRunner preserves remote.New's SSH configuration and replaces the remote command.
type tunnelRunner struct {
	runner runexec.Runner
	port   int
}

func (r tunnelRunner) Run(ctx context.Context, c runexec.Cmd) (runexec.Result, error) {
	// remote.Run appends exactly one quoted command after the destination.
	c.Args = append([]string(nil), c.Args[:len(c.Args)-1]...)
	index := len(c.Args) - 2 // the -- delimiter before the destination
	forward := "127.0.0.1:" + strconv.Itoa(r.port) + ":127.0.0.1:3000"
	flags := []string{"-N", "-o", "ExitOnForwardFailure=yes", "-L", forward}
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
	vm := remote.New(h, tunnelRunner{runner: runner, port: port}, remote.Options{AcceptNewHostKey: o.AcceptNewHostKey, DisableMultiplexing: true})
	type outcome struct {
		result runexec.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		res, e := vm.Run(ctx, remote.Cmd{Argv: []string{"true"}, Stderr: o.ErrOut})
		done <- outcome{res, e}
	}()
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
	client := &http.Client{Timeout: 250 * time.Millisecond, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	tunnelFailure := func(action string, cause error, result runexec.Result) error {
		if stderr := strings.TrimSpace(string(result.Stderr)); stderr != "" {
			cause = fmt.Errorf("%w: %s", cause, stderr)
		}
		return failure(action, cause)
	}
	ready := false
	for !ready {
		select {
		case result := <-done:
			exited = true
			if result.err == nil {
				result.err = errors.New("SSH exited before the tunnel was ready")
			}
			return tunnelFailure("open Grafana tunnel", result.err, result.result)
		case <-ctx.Done():
			return nil
		case <-timeout.C:
			cancel()
			result := <-done
			exited = true
			return tunnelFailure("open Grafana tunnel", errors.New("the Grafana health request through SSH did not succeed"), result.result)
		case <-ticker.C:
			ready = grafanaReady(ctx, client, address)
		}
	}
	url := "http://" + address
	fmt.Fprintf(o.Out, "%s\nPress Ctrl-C to close the tunnel.\n", url)
	if !o.NoBrowser {
		if err = browser.Open(ctx, runner, url); err != nil {
			fmt.Fprintln(o.ErrOut, "Cannot open the browser. Open the printed URL manually.")
		}
	}
	select {
	case <-ctx.Done():
		return nil
	case result := <-done:
		exited = true
		if ctx.Err() != nil {
			return nil
		}
		if result.err == nil {
			result.err = errors.New("SSH exited while the tunnel was open")
		}
		return tunnelFailure("run Grafana tunnel", result.err, result.result)
	}
}

func grafanaReady(ctx context.Context, client *http.Client, address string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/api/health", nil)
	if err != nil {
		return false
	}
	res, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = res.Body.Close() }()
	var health struct {
		Database string `json:"database"`
	}
	return res.StatusCode == http.StatusOK && json.NewDecoder(res.Body).Decode(&health) == nil && health.Database == "ok"
}
