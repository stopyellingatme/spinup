package monitor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGrafanaReady(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, body string
		status     int
		err        error
		want       bool
	}{
		{name: "healthy", body: `{"database":"ok","version":"13"}`, status: 200, want: true},
		{name: "database unavailable", body: `{"database":"failing"}`, status: 200},
		{name: "unavailable", body: `{"database":"ok"}`, status: 503},
		{name: "unrelated listener", body: `<html>hello</html>`, status: 200},
		{name: "forward refused", err: errors.New("connection reset")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := &trackedBody{Reader: strings.NewReader(tt.body)}
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "http://127.0.0.1:12345/api/health" {
					t.Errorf("health URL = %s", r.URL)
				}
				if tt.err != nil {
					return nil, tt.err
				}
				return &http.Response{StatusCode: tt.status, Body: body, Header: make(http.Header)}, nil
			})}
			if got := grafanaReady(context.Background(), client, "127.0.0.1:12345"); got != tt.want {
				t.Fatalf("ready = %v, want %v", got, tt.want)
			}
			if tt.err == nil && !body.closed {
				t.Error("health response was not closed")
			}
		})
	}
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error { b.closed = true; return nil }

type tunnelFunc func(context.Context, runexec.Cmd) (runexec.Result, error)

func (f tunnelFunc) Run(ctx context.Context, c runexec.Cmd) (runexec.Result, error) { return f(ctx, c) }

type readyOutput struct {
	bytes.Buffer
	healthy *atomic.Bool
	early   bool
	cancel  context.CancelFunc
}

func (w *readyOutput) Write(b []byte) (int, error) {
	if !w.healthy.Load() {
		w.early = true
	}
	n, err := w.Buffer.Write(b)
	w.cancel()
	return n, err
}

func requireTunnelSocket(t *testing.T) {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local sockets unavailable: %v", err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMonitorOpenWaitsForGrafanaHealth(t *testing.T) {
	requireTunnelSocket(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var healthy atomic.Bool
	var requests atomic.Int32
	out := &readyOutput{healthy: &healthy, cancel: cancel}
	calls := 0
	runner := tunnelFunc(func(ctx context.Context, c runexec.Cmd) (runexec.Result, error) {
		calls++
		all := strings.Join(c.Args, " ")
		if !strings.Contains(all, "ControlMaster=no -o ControlPath=none -o ControlPersist=no") {
			return runexec.Result{}, fmt.Errorf("shared SSH connection: %s", all)
		}
		address := ""
		for i, arg := range c.Args {
			if arg == "-L" {
				address = strings.TrimSuffix(c.Args[i+1], ":127.0.0.1:3000")
			}
		}
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
		if err != nil {
			return runexec.Result{}, err
		}
		server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/health" {
				t.Errorf("path = %s", r.URL.Path)
			}
			switch requests.Add(1) {
			case 1:
				w.WriteHeader(http.StatusServiceUnavailable)
			case 2:
				_, _ = io.WriteString(w, "not Grafana")
			default:
				healthy.Store(true)
				_, _ = io.WriteString(w, `{"database":"ok"}`)
			}
		})}
		done := make(chan struct{})
		go func() { defer close(done); _ = server.Serve(listener) }()
		<-ctx.Done()
		_ = server.Close()
		<-done
		return runexec.Result{}, ctx.Err()
	})
	err := open(ctx, Options{NoBrowser: true, Out: out, ErrOut: io.Discard}, remote.Host{Address: "vm"}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if out.early || !strings.Contains(out.String(), "http://127.0.0.1:") || requests.Load() < 3 {
		t.Fatalf("output=%q, early=%v, requests=%d", out.String(), out.early, requests.Load())
	}
	if calls != 1 {
		t.Fatalf("SSH calls = %d, want one owned process", calls)
	}
}

func TestMonitorOpenReportsSSHStderr(t *testing.T) {
	requireTunnelSocket(t)
	f := &runexec.Fake{}
	f.Expect("ssh").Return("", "forwarding request rejected", 0)
	err := open(context.Background(), Options{Out: io.Discard, ErrOut: io.Discard}, remote.Host{Address: "vm"}, f)
	if err == nil || !strings.Contains(err.Error(), "forwarding request rejected") {
		t.Fatalf("error = %v", err)
	}
	f.AssertDone(t)
}

// serveForward plays the local side of "ssh -L": it listens on the forwarded local address.
func serveForward(ctx context.Context, args []string, h http.Handler) (io.Closer, error) {
	address := ""
	for i, arg := range args {
		if arg == "-L" && i+1 < len(args) {
			local, _, _ := strings.Cut(strings.TrimPrefix(args[i+1], "127.0.0.1:"), ":")
			address = "127.0.0.1:" + local
		}
	}
	if address == "" {
		return nil, errors.New("no -L forward in the SSH arguments")
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: h}
	go func() { _ = server.Serve(listener) }()
	return server, nil
}

func TestMonitorOpenTimesOutWithoutGrafana(t *testing.T) {
	requireTunnelSocket(t)
	old := openStartup
	openStartup = 300 * time.Millisecond
	t.Cleanup(func() { openStartup = old })
	var stopped atomic.Bool
	runner := tunnelFunc(func(ctx context.Context, c runexec.Cmd) (runexec.Result, error) {
		server, err := serveForward(ctx, c.Args, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		if err != nil {
			return runexec.Result{}, err
		}
		<-ctx.Done()
		_ = server.Close()
		stopped.Store(true)
		return runexec.Result{}, ctx.Err()
	})
	var out bytes.Buffer
	err := open(context.Background(), Options{NoBrowser: true, Out: &out, ErrOut: io.Discard}, remote.Host{Address: "vm"}, runner)
	if err == nil || !strings.Contains(err.Error(), "the Grafana health request through SSH did not succeed") {
		t.Fatalf("error = %v", err)
	}
	if out.Len() != 0 || !stopped.Load() {
		t.Fatalf("output=%q, SSH stopped=%v", out.String(), stopped.Load())
	}
}

type signalOutput struct {
	bytes.Buffer
	once  sync.Once
	ready chan struct{}
}

func (w *signalOutput) Write(b []byte) (int, error) {
	n, err := w.Buffer.Write(b)
	w.once.Do(func() { close(w.ready) })
	return n, err
}

func TestMonitorOpenReportsSSHExitWhileOpen(t *testing.T) {
	requireTunnelSocket(t)
	old := openAlive
	openAlive = 20 * time.Millisecond
	t.Cleanup(func() { openAlive = old })
	out := &signalOutput{ready: make(chan struct{})}
	runner := tunnelFunc(func(ctx context.Context, c runexec.Cmd) (runexec.Result, error) {
		server, err := serveForward(ctx, c.Args, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"database":"ok"}`)
		}))
		if err != nil {
			return runexec.Result{}, err
		}
		select {
		case <-out.ready:
		case <-ctx.Done():
		}
		_ = server.Close()
		return runexec.Result{Stderr: []byte("Connection to vm closed by remote host."), ExitCode: 255}, &runexec.ExitError{Code: 255}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := open(ctx, Options{NoBrowser: true, Out: out, ErrOut: io.Discard}, remote.Host{Address: "vm"}, runner)
	if err == nil || !strings.Contains(err.Error(), "Connection to vm closed by remote host.") || ctx.Err() != nil {
		t.Fatalf("error = %v", err)
	}
}
