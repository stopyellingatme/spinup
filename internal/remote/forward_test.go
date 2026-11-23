package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

// fakeSSHEnv makes the test binary act as an ssh process that serves its -L forward.
const fakeSSHEnv = "SPINUP_TEST_FAKE_SSH_FORWARD"

func TestMain(m *testing.M) {
	if os.Getenv(fakeSSHEnv) == "1" {
		os.Exit(fakeSSHMain(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeSSHMain listens on the local side of the -L argument and serves /healthz until it is killed.
func fakeSSHMain(args []string) int {
	address, err := forwardAddress(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 255
	}
	listener, err := listen(address)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bind [%s]: Address already in use\n", address)
		return 255
	}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})}
	_ = server.Serve(listener)
	return 0
}

// forwardAddress returns the local address of the -L argument.
func forwardAddress(args []string) (string, error) {
	for i, arg := range args {
		if arg == "-L" && i+1 < len(args) {
			parts := strings.Split(args[i+1], ":")
			if len(parts) == 4 {
				return parts[0] + ":" + parts[1], nil
			}
		}
	}
	return "", errors.New("no -L forward in the SSH arguments")
}

type runnerFunc func(context.Context, runexec.Cmd) (runexec.Result, error)

func (f runnerFunc) Run(ctx context.Context, c runexec.Cmd) (runexec.Result, error) { return f(ctx, c) }

func requireLoopback(t *testing.T) {
	t.Helper()
	l, err := listen("127.0.0.1:0")
	if err != nil {
		t.Skipf("local sockets unavailable: %v", err)
	}
	_ = l.Close()
}

// fakeVM plays ssh: it serves the forwarded port with h until its context ends.
type fakeVM struct {
	h       http.Handler
	mu      sync.Mutex
	calls   [][]string
	running atomic.Int32
}

func (v *fakeVM) Run(ctx context.Context, c runexec.Cmd) (runexec.Result, error) {
	v.mu.Lock()
	v.calls = append(v.calls, append([]string(nil), c.Args...))
	v.mu.Unlock()
	address, err := forwardAddress(c.Args)
	if err != nil {
		return runexec.Result{}, err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", address)
	if err != nil {
		return runexec.Result{Stderr: []byte("bind [" + address + "]: Address already in use"), ExitCode: 255}, &runexec.ExitError{Code: 255}
	}
	v.running.Add(1)
	defer v.running.Add(-1)
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: v.h}
	go func() { _ = server.Serve(listener) }()
	<-ctx.Done()
	_ = server.Close()
	return runexec.Result{}, ctx.Err()
}

func (v *fakeVM) Calls() [][]string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.calls)
}

func get(t *testing.T, port int, path string) (string, error) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+path, nil)
	if err != nil {
		return "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	return string(body), err
}

func text(s string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, s) })
}

func TestForwardLoopbackServesAndCloses(t *testing.T) {
	requireLoopback(t)
	vm := &fakeVM{h: text("console")}
	host := Host{Name: "production", Address: "vm", User: "deploy", HostKeys: []string{testKey(7)}}
	port, closeForward, err := ForwardLoopback(context.Background(), host, vm, Options{CacheDir: shortCache(t), AcceptNewHostKey: true}, 7410)
	if err != nil {
		t.Fatal(err)
	}
	if body, err := get(t, port, "/healthz"); err != nil || body != "console" {
		t.Fatalf("GET through the forward = %q, %v", body, err)
	}
	calls := vm.Calls()
	if len(calls) != 1 {
		t.Fatalf("SSH calls = %d", len(calls))
	}
	all := strings.Join(calls[0], " ")
	for _, want := range []string{
		"-o ControlMaster=no -o ControlPath=none -o ControlPersist=no",
		"-o StrictHostKeyChecking=yes", "-o GlobalKnownHostsFile=/dev/null",
		"-N -o ExitOnForwardFailure=yes -L 127.0.0.1:" + strconv.Itoa(port) + ":127.0.0.1:7410 -- deploy@vm",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("argv %q misses %q", all, want)
		}
	}
	// The pin wins over first-use acceptance.
	if strings.Contains(all, "accept-new") {
		t.Errorf("argv %q accepts a new host key despite the pin", all)
	}
	if err = closeForward(); err != nil {
		t.Fatal(err)
	}
	if err = closeForward(); err != nil {
		t.Fatalf("second close = %v", err)
	}
	if vm.running.Load() != 0 {
		t.Fatal("close returned before SSH stopped")
	}
	if _, err = get(t, port, "/healthz"); err == nil {
		t.Fatal("the forward still answers after close")
	}
}

func TestForwardLoopbackRetriesOccupiedPort(t *testing.T) {
	requireLoopback(t)
	vm := &fakeVM{h: text("console")}
	var occupied net.Listener
	t.Cleanup(func() {
		if occupied != nil {
			_ = occupied.Close()
		}
	})
	var calls atomic.Int32
	runner := runnerFunc(func(ctx context.Context, c runexec.Cmd) (runexec.Result, error) {
		if calls.Add(1) == 1 {
			// Another process takes the port between the reservation and the SSH bind.
			address, err := forwardAddress(c.Args)
			if err != nil {
				return runexec.Result{}, err
			}
			if occupied, err = listen(address); err != nil {
				return runexec.Result{}, err
			}
		}
		return vm.Run(ctx, c)
	})
	port, closeForward, err := ForwardLoopback(context.Background(), Host{Address: "vm"}, runner, Options{CacheDir: shortCache(t)}, 7410)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeForward() }()
	if calls.Load() != 2 {
		t.Fatalf("SSH calls = %d, want a retry after the collision", calls.Load())
	}
	if occupied == nil || port == occupied.Addr().(*net.TCPAddr).Port {
		t.Fatalf("forward port %d is the occupied port", port)
	}
	if body, err := get(t, port, "/"); err != nil || body != "console" {
		t.Fatalf("GET = %q, %v", body, err)
	}
}

func TestForwardLoopbackSSHExitsEarly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake ssh is a sh script; remote commands need WSL2")
	}
	requireLoopback(t)
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	script := "#!/bin/sh\necho call >> " + Quote(count) + "\necho 'Host key verification failed.' >&2\nexit 255\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	started := time.Now()
	_, closeForward, err := ForwardLoopback(context.Background(), Host{Address: "vm"}, runexec.OSRunner{}, Options{CacheDir: shortCache(t)}, 7410)
	if err == nil || closeForward != nil {
		t.Fatal("forward succeeded after SSH exited")
	}
	for _, want := range []string{"cannot forward VM port 7410", "SSH rejected the host key", "Host key verification failed.", "Try:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q misses %q", err, want)
		}
	}
	if data, _ := os.ReadFile(count); strings.Count(string(data), "call") != 1 {
		t.Fatalf("SSH calls = %q, want no retry after a host key failure", data)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("the failure waited for the startup bound")
	}
}

func TestForwardLoopbackProcessLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake ssh is a sh script; remote commands need WSL2")
	}
	requireLoopback(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nexec " + Quote(self) + " \"$@\"\n"
	if err = os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(fakeSSHEnv, "1")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	port, closeForward, err := ForwardLoopback(ctx, Host{Address: "vm"}, runexec.OSRunner{}, Options{CacheDir: shortCache(t)}, 7410)
	if err != nil {
		t.Fatal(err)
	}
	if body, err := get(t, port, "/healthz"); err != nil || body != "ok\n" {
		t.Fatalf("GET = %q, %v", body, err)
	}
	if err = closeForward(); err != nil {
		t.Fatal(err)
	}
	if _, err = get(t, port, "/healthz"); err == nil {
		t.Fatal("the SSH process still serves after close")
	}
}

func TestForwardLoopbackCancellationKeepsOtherForward(t *testing.T) {
	requireLoopback(t)
	first, second := &fakeVM{h: text("first")}, &fakeVM{h: text("second")}
	ctx, cancel := context.WithCancel(context.Background())
	portA, closeA, err := ForwardLoopback(ctx, Host{Address: "vm"}, first, Options{CacheDir: shortCache(t)}, 7410)
	if err != nil {
		t.Fatal(err)
	}
	portB, closeB, err := ForwardLoopback(context.Background(), Host{Address: "vm"}, second, Options{CacheDir: shortCache(t)}, 7410)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeB() }()
	cancel()
	if err = closeA(); err != nil {
		t.Fatalf("close after cancellation = %v", err)
	}
	if first.running.Load() != 0 {
		t.Fatal("the cancelled forward still runs")
	}
	if _, err = get(t, portA, "/"); err == nil {
		t.Fatal("the cancelled forward still answers")
	}
	if body, err := get(t, portB, "/"); err != nil || body != "second" {
		t.Fatalf("the other forward = %q, %v", body, err)
	}
	if err = closeB(); err != nil {
		t.Fatal(err)
	}
}

func TestForwardLoopbackStartupBound(t *testing.T) {
	requireLoopback(t)
	old := forwardStartup
	forwardStartup = 200 * time.Millisecond
	t.Cleanup(func() { forwardStartup = old })
	var stopped atomic.Bool
	runner := runnerFunc(func(ctx context.Context, _ runexec.Cmd) (runexec.Result, error) {
		<-ctx.Done() // SSH hangs before it binds the local port.
		stopped.Store(true)
		return runexec.Result{Stderr: []byte("debug: connecting")}, ctx.Err()
	})
	_, _, err := ForwardLoopback(context.Background(), Host{Address: "vm"}, runner, Options{CacheDir: shortCache(t)}, 7410)
	if err == nil || !strings.Contains(err.Error(), "SSH did not open the forward within 200ms") {
		t.Fatalf("error = %v", err)
	}
	if !stopped.Load() {
		t.Fatal("a failed acquisition left SSH running")
	}
}

func TestForwardLoopbackSSHExitReportedByClose(t *testing.T) {
	requireLoopback(t)
	exit := make(chan struct{})
	runner := runnerFunc(func(ctx context.Context, c runexec.Cmd) (runexec.Result, error) {
		address, err := forwardAddress(c.Args)
		if err != nil {
			return runexec.Result{}, err
		}
		listener, err := listen(address)
		if err != nil {
			return runexec.Result{}, err
		}
		<-exit
		_ = listener.Close()
		return runexec.Result{Stderr: []byte("Connection to vm closed by remote host."), ExitCode: 255}, &runexec.ExitError{Code: 255}
	})
	_, closeForward, err := ForwardLoopback(context.Background(), Host{Address: "vm"}, runner, Options{CacheDir: shortCache(t)}, 7410)
	if err != nil {
		t.Fatal(err)
	}
	close(exit)
	err = closeForward()
	if err == nil || !strings.Contains(err.Error(), "Connection to vm closed by remote host.") {
		t.Fatalf("close = %v", err)
	}
	if again := closeForward(); again == nil || again.Error() != err.Error() {
		t.Fatalf("second close = %v", again)
	}
}

func TestForwardLoopbackRejectsPort(t *testing.T) {
	t.Parallel()
	for _, port := range []int{0, -1, 65536} {
		if _, _, err := ForwardLoopback(context.Background(), Host{Address: "vm"}, &runexec.Fake{}, Options{}, port); err == nil {
			t.Errorf("port %d accepted", port)
		}
	}
}

// listen opens a local TCP listener.
func listen(address string) (net.Listener, error) {
	return (&net.ListenConfig{}).Listen(context.Background(), "tcp4", address)
}
