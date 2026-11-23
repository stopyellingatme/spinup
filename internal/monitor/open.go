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

// openStartup bounds the forward and the Grafana health check. Tests shorten it.
var openStartup = 10 * time.Second

// openAlive is the interval of the forward liveness check while the tunnel is open.
var openAlive = 2 * time.Second

func open(ctx context.Context, o Options, h remote.Host, runner runexec.Runner) error {
	started := time.Now()
	port, closeForward, err := remote.ForwardLoopback(ctx, h, runner, remote.Options{AcceptNewHostKey: o.AcceptNewHostKey}, 3000)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		// The forward error keeps the SSH diagnostics and its own Try line.
		return fmt.Errorf("open Grafana tunnel failed: %w", err)
	}
	// Close only the forward of this invocation, also after cancellation or a browser failure.
	defer func() { _ = closeForward() }()
	address := "127.0.0.1:" + strconv.Itoa(port)
	client := &http.Client{Timeout: 250 * time.Millisecond, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(openStartup - time.Since(started))
	defer timeout.Stop()
	for ready := false; !ready; {
		select {
		case <-ctx.Done():
			return nil
		case <-timeout.C:
			cause := errors.New("the Grafana health request through SSH did not succeed")
			if cerr := closeForward(); cerr != nil {
				cause = fmt.Errorf("%w: %s", cause, withoutTry(cerr.Error()))
			}
			return failure("open Grafana tunnel", cause)
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
	alive := time.NewTicker(openAlive)
	defer alive.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-alive.C:
			if forwardListening(ctx, address) {
				continue
			}
			cerr := closeForward()
			if ctx.Err() != nil {
				return nil
			}
			if cerr == nil {
				cerr = errors.New("SSH exited while the tunnel was open")
			}
			return failure("run Grafana tunnel", errors.New(withoutTry(cerr.Error())))
		}
	}
}

// forwardListening reports whether the local SSH listener still accepts connections.
// SSH accepts a connection even when the VM service refuses it, so a refusal means SSH exited.
func forwardListening(ctx context.Context, address string) bool {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp4", address)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// withoutTry removes an embedded Try line before the error gets the Try line of failure.
func withoutTry(s string) string {
	before, _, _ := strings.Cut(s, "\nTry:")
	return before
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
