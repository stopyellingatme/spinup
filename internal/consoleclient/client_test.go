package consoleclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// goldenOverview is an inline overview v1 response. It mixes every section state, null
// values, additive keys, and a C1 control character, which the text form must escape.
const goldenOverview = `{"version":1,"app":"todo","env":"production","profile":"lite","observed_at":"2026-11-22T18:00:00Z",
 "release":{"state":"ok","reason":null,"at":"2026-11-22T18:00:00Z","data":{"id":"20261122T170000Z-ab12cd3","git_sha":"ab12cd3ef0123456789abcdef0123456789abcde","color":"blue","created_at":"2026-11-22T17:00:00Z","age_seconds":3600,"previous_id":null}},
 "containers":{"state":"ok","reason":null,"at":"2026-11-22T18:00:00Z","data":[{"project":"todo","service":"caddy","name":"todo-caddy-1","state":"running","health":null,"image":"caddy:2"},{"project":"todo-blue","service":"api","name":"todo-blue-api-1","state":"exited","health":"unhealthy"}]},
 "alerts":{"state":"unavailable","reason":"source_down","at":"2026-11-22T18:00:05Z","data":null},
 "backup":{"state":"ok","reason":null,"at":"2026-11-22T18:00:00Z","data":null},
 "wal":{"state":"stale","reason":"sample_expired","at":"2026-11-22T17:58:00Z","data":{"state":"archiving","archived_at":"2026-11-22T17:57:00Z","failed_at":null,"min_segment":null,"max_segment":null}},
 "jobs":{"state":"disabled","reason":"no_jobs","at":null,"data":null},
 "disk":{"state":"ok","reason":null,"at":"2026-11-22T18:00:00Z","data":{"total_bytes":1000,"used_bytes":400,"available_bytes":500}},
 "memory":{"state":"ok","reason":null,"at":"2026-11-22T18:00:00Z","data":{"total_bytes":4096,"used_bytes":1024,"available_bytes":3072}},
 "history":{"state":"ok","reason":null,"at":"2026-11-22T18:00:00Z","data":[{"id":"20261122T170000Z-ab12cd3","at":"2026-11-22T17:00:00Z","user":"op` + "\u009b" + `","git_sha":null,"color":null,"result":"failed"}]},
 "future":{"additive":true}}
`

const goldenText = `todo (production)
Observed: 2026-11-22T18:00:00Z
Live release
  id="20261122T170000Z-ab12cd3"
  git_sha="ab12cd3ef0123456789abcdef0123456789abcde"
  color="blue"
  created_at="2026-11-22T17:00:00Z"
  age_seconds=3600
  previous_id=null
Containers
  {"project":"todo","service":"caddy","name":"todo-caddy-1","state":"running","health":null}
  {"project":"todo-blue","service":"api","name":"todo-blue-api-1","state":"exited","health":"unhealthy"}
Firing alerts [unavailable: source_down]
Last backup
  none
WAL archive [stale: sample_expired]
  state="archiving"
  archived_at="2026-11-22T17:57:00Z"
  failed_at=null
  min_segment=null
  max_segment=null
Job queues [disabled: no_jobs]
Disk
  total_bytes=1000
  used_bytes=400
  available_bytes=500
Memory
  total_bytes=4096
  used_bytes=1024
  available_bytes=3072
Deploy history
  {"id":"20261122T170000Z-ab12cd3","at":"2026-11-22T17:00:00Z","user":"op\u009b","git_sha":null,"color":null,"result":"failed"}
`

func requireLoopback(t *testing.T) {
	t.Helper()
	l, err := listen("127.0.0.1:0")
	if err != nil {
		t.Skipf("local sockets unavailable: %v", err)
	}
	_ = l.Close()
}

// project writes a spinup.toml and gives SSH a short cache directory.
func writeProject(t *testing.T, modules string) string {
	t.Helper()
	requireLoopback(t)
	// The SSH control path limit leaves room for a short home directory only.
	home, err := os.MkdirTemp("/tmp", "c")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("SPINUP_SSH_CONFIG", "")
	dir := t.TempDir()
	toml := "modules=[" + modules + "]\n[app]\nname='todo'\nmodule='example.com/todo'\n[env.production]\nhost='vm'\nuser='deploy'\n"
	if err = os.WriteFile(filepath.Join(dir, "spinup.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// seams replaces Refresh and the browser opener, and shortens the bounds.
type seams struct {
	mu       sync.Mutex
	refresh  []string
	urls     []string
	failWith error
	browser  func(string) error
}

func install(t *testing.T) *seams {
	t.Helper()
	s := &seams{}
	oldRefresh, oldBrowser, oldStartup, oldRefreshEvery, oldAlive := refresh, openBrowser, startupBound, refreshInterval, aliveInterval
	t.Cleanup(func() {
		refresh, openBrowser, startupBound, refreshInterval, aliveInterval = oldRefresh, oldBrowser, oldStartup, oldRefreshEvery, oldAlive
	})
	refresh = func(_ context.Context, vm remote.Runner, app string) error {
		if vm == nil {
			return errors.New("no remote runner")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.refresh = append(s.refresh, app)
		return s.failWith
	}
	openBrowser = func(_ context.Context, _ runexec.Runner, url string) error {
		s.mu.Lock()
		s.urls = append(s.urls, url)
		hook := s.browser
		s.mu.Unlock()
		if hook != nil {
			return hook(url)
		}
		return nil
	}
	startupBound = 2 * time.Second
	return s
}

func (s *seams) refreshes() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.refresh) }

// vm plays ssh: each -L forward serves the handler of its VM port. A missing handler
// accepts and closes connections, like SSH when nothing listens on the VM port.
type vm struct {
	handlers map[int]http.Handler
	// before runs ahead of a forward and can replace its result.
	before  func(call int, port int, address string) (res runexec.Result, handled bool, err error)
	mu      sync.Mutex
	calls   [][]string
	running atomic.Int32
}

func (v *vm) Run(ctx context.Context, c runexec.Cmd) (runexec.Result, error) {
	v.mu.Lock()
	v.calls = append(v.calls, append([]string(nil), c.Args...))
	call := len(v.calls)
	v.mu.Unlock()
	if c.Name != "ssh" {
		return runexec.Result{}, errors.New("unexpected command " + c.Name)
	}
	address, port := "", 0
	for i, arg := range c.Args {
		if arg == "-L" && i+1 < len(c.Args) {
			parts := strings.Split(c.Args[i+1], ":")
			address = parts[0] + ":" + parts[1]
			port, _ = strconv.Atoi(parts[3])
		}
	}
	if address == "" {
		return runexec.Result{}, errors.New("no forward")
	}
	if v.before != nil {
		if res, handled, err := v.before(call, port, address); handled {
			return res, err
		}
	}
	listener, err := listen(address)
	if err != nil {
		return runexec.Result{Stderr: []byte("bind [" + address + "]: Address already in use"), ExitCode: 255}, &runexec.ExitError{Code: 255}
	}
	v.running.Add(1)
	defer v.running.Add(-1)
	if h := v.handlers[port]; h != nil {
		server := &http.Server{ReadHeaderTimeout: time.Second, Handler: h}
		go func() { _ = server.Serve(listener) }()
		<-ctx.Done()
		_ = server.Close()
	} else {
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				_ = conn.Close()
			}
		}()
		<-ctx.Done()
		_ = listener.Close()
	}
	return runexec.Result{}, ctx.Err()
}

func (v *vm) forwards() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []string
	for _, args := range v.calls {
		out = append(out, strings.Join(args, " "))
	}
	return out
}

type service struct {
	health   atomic.Int32
	overview string
	status   int
}

func (s *service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		s.health.Add(1)
		if s.status != 0 && s.status != http.StatusOK {
			w.WriteHeader(s.status)
			return
		}
		_, _ = io.WriteString(w, "ok\n")
	case "/api/v1/overview":
		_, _ = io.WriteString(w, s.overview)
	default:
		http.NotFound(w, r)
	}
}

type grafana struct{ health atomic.Int32 }

func (g *grafana) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/health" {
		g.health.Add(1)
		_, _ = io.WriteString(w, `{"database":"ok","version":"13.0.0"}`)
		return
	}
	_, _ = io.WriteString(w, "grafana")
}

func status(t *testing.T, dir string, asJSON bool, v *vm) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := Status(ctx, Options{Dir: dir, Env: "production", JSON: asJSON, Out: &out, ErrOut: &errOut}, v)
	if v.running.Load() != 0 {
		t.Error("Status returned before its forward closed")
	}
	return out.String(), errOut.String(), err
}

func TestConsoleStatusText(t *testing.T) {
	dir := writeProject(t, "'deploy','console','monitoring'")
	s := install(t)
	v := &vm{handlers: map[int]http.Handler{consolePort: &service{overview: goldenOverview}}}
	out, stderr, err := status(t, dir, false, v)
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if out != goldenText {
		t.Fatalf("text output:\n%s\nwant:\n%s", out, goldenText)
	}
	if s.refreshes() != 1 || s.refresh[0] != "todo" {
		t.Fatalf("refresh calls = %q", s.refresh)
	}
	forwards := v.forwards()
	if len(forwards) != 1 || !strings.Contains(forwards[0], ":127.0.0.1:7410 -- deploy@vm") || !strings.Contains(forwards[0], "ControlPath=none") {
		t.Fatalf("forwards = %q", forwards)
	}
}

func TestConsoleStatusJSON(t *testing.T) {
	dir := writeProject(t, "'deploy','console'")
	install(t)
	// Non-canonical spacing and additive keys survive. The service sends extra newlines.
	body := strings.Replace(goldenOverview, `"version":1`, `"version" : 1`, 1) + "\n\n"
	v := &vm{handlers: map[int]http.Handler{consolePort: &service{overview: body}}}
	out, stderr, err := status(t, dir, true, v)
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if want := strings.TrimRight(body, "\n") + "\n"; out != want {
		t.Fatalf("stdout = %q, want the received bytes and one newline", out)
	}
	dec := json.NewDecoder(strings.NewReader(out))
	var doc map[string]any
	if err = dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if _, err = dec.Token(); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout has more than one document: %v", err)
	}
}

func TestConsoleStatusRefreshFailureReadsPriorOverview(t *testing.T) {
	dir := writeProject(t, "'deploy','console','monitoring'")
	s := install(t)
	s.failWith = errors.New("cannot refresh console facts: the sampler failed\nTry: retry")
	v := &vm{handlers: map[int]http.Handler{consolePort: &service{overview: goldenOverview}}}
	out, stderr, err := status(t, dir, false, v)
	if err != nil {
		t.Fatal(err)
	}
	if out != goldenText {
		t.Fatalf("the prior overview changed:\n%s", out)
	}
	if !strings.Contains(stderr, "Console sample delayed: cannot refresh console facts: the sampler failed.") || strings.Contains(stderr, "Try: retry") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestConsoleStatusIncompatible(t *testing.T) {
	big := strings.Replace(goldenOverview, `"future":{"additive":true}`, `"future":"`+strings.Repeat("x", overviewLimit)+`"`, 1)
	for name, body := range map[string]string{
		"wrong env":          strings.Replace(goldenOverview, `"env":"production"`, `"env":"staging"`, 1),
		"wrong app":          strings.Replace(goldenOverview, `"app":"todo"`, `"app":"other"`, 1),
		"version":            strings.Replace(goldenOverview, `"version":1`, `"version":2`, 1),
		"missing section":    strings.Replace(goldenOverview, `"jobs":`, `"jobs_old":`, 1),
		"section state":      strings.Replace(goldenOverview, `"state":"disabled"`, `"state":"off"`, 1),
		"reason":             strings.Replace(goldenOverview, `"reason":"no_jobs"`, `"reason":"later"`, 1),
		"ok with reason":     strings.Replace(goldenOverview, `"state":"ok","reason":null,"at":"2026-11-22T18:00:00Z","data":null`, `"state":"ok","reason":"missing","at":"2026-11-22T18:00:00Z","data":null`, 1),
		"unavailable data":   strings.Replace(goldenOverview, `"reason":"source_down","at":"2026-11-22T18:00:05Z","data":null`, `"reason":"source_down","at":"2026-11-22T18:00:05Z","data":[]`, 1),
		"null array":         strings.Replace(goldenOverview, `"data":[{"project"`, `"data":null,"x":[{"project"`, 1),
		"record enum":        strings.Replace(goldenOverview, `"color":"blue"`, `"color":"red"`, 1),
		"local time":         strings.Replace(goldenOverview, `"created_at":"2026-11-22T17:00:00Z"`, `"created_at":"2026-11-22T11:00:00-06:00"`, 1),
		"negative bytes":     strings.Replace(goldenOverview, `"used_bytes":400`, `"used_bytes":-400`, 1),
		"fractional count":   strings.Replace(goldenOverview, `"age_seconds":3600`, `"age_seconds":3600.5`, 1),
		"missing field":      strings.Replace(goldenOverview, `,"previous_id":null`, ``, 1),
		"two documents":      goldenOverview + `{"version":1}`,
		"not json":           "<html>console</html>",
		"over 512 KiB":       big,
		"invalid utf-8":      strings.Replace(goldenOverview, "op\u009b", "op\xff", 1),
		"missing observed":   strings.Replace(goldenOverview, `"observed_at":"2026-11-22T18:00:00Z",`, ``, 1),
		"disabled with time": strings.Replace(goldenOverview, `"reason":"no_jobs","at":null`, `"reason":"no_jobs","at":"2026-11-22T18:00:00Z"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeProject(t, "'deploy','console'")
			install(t)
			if body == goldenOverview {
				t.Fatal("the mutation did not apply")
			}
			v := &vm{handlers: map[int]http.Handler{consolePort: &service{overview: body}}}
			for _, asJSON := range []bool{false, true} {
				out, _, err := status(t, dir, asJSON, v)
				var ue *ui.Error
				if !errors.As(err, &ue) || ue.What != "Cannot read console: the overview has an incompatible version, identity, or shape." || ue.Try != "upgrade the CLI and deploy the console for production" {
					t.Fatalf("error = %#v", err)
				}
				if out != "" {
					t.Fatalf("stdout = %q, want no partial output", out)
				}
			}
		})
	}
}

func TestConsoleStatusAcceptsNullObservation(t *testing.T) {
	dir := writeProject(t, "'deploy','console'")
	install(t)
	body := `{"version":1,"app":"todo","env":"production","profile":"absent","observed_at":null,` +
		`"release":{"state":"unavailable","reason":"missing","at":null,"data":null},` +
		`"containers":{"state":"unavailable","reason":"missing","at":null,"data":null},` +
		`"alerts":{"state":"disabled","reason":"no_monitoring","at":null,"data":null},` +
		`"backup":{"state":"disabled","reason":"no_backups","at":null,"data":null},` +
		`"wal":{"state":"disabled","reason":"no_backups","at":null,"data":null},` +
		`"jobs":{"state":"ok","reason":null,"at":"2026-11-22T18:00:00Z","data":[]},` +
		`"disk":{"state":"unavailable","reason":"timeout","at":"2026-11-22T18:00:00Z","data":null},` +
		`"memory":{"state":"unavailable","reason":"invalid","at":"2026-11-22T18:00:00Z","data":null},` +
		`"history":{"state":"stale","reason":"sample_expired","at":"2026-11-22T17:00:00Z","data":[]}}`
	v := &vm{handlers: map[int]http.Handler{consolePort: &service{overview: body}}}
	out, stderr, err := status(t, dir, false, v)
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	want := "todo (production)\nObserved: unknown\nLive release [unavailable: missing]\nContainers [unavailable: missing]\n" +
		"Firing alerts [disabled: no_monitoring]\nLast backup [disabled: no_backups]\nWAL archive [disabled: no_backups]\n" +
		"Job queues\n  none\nDisk [unavailable: timeout]\nMemory [unavailable: invalid]\nDeploy history [stale: sample_expired]\n  none\n"
	if out != want {
		t.Fatalf("text output:\n%s\nwant:\n%s", out, want)
	}
}

func TestConsoleStatusDisabledModule(t *testing.T) {
	dir := writeProject(t, "'deploy','monitoring'")
	s := install(t)
	v := &vm{}
	out, _, err := status(t, dir, true, v)
	var ue *ui.Error
	if !errors.As(err, &ue) || ue.What != "Cannot open console: module console is disabled." || ue.Try != "spinup module add console" {
		t.Fatalf("error = %#v", err)
	}
	if out != "" || len(v.forwards()) != 0 || s.refreshes() != 0 {
		t.Fatalf("stdout=%q forwards=%q refreshes=%d", out, v.forwards(), s.refreshes())
	}
}

func TestConsoleStatusSSHExitsEarly(t *testing.T) {
	dir := writeProject(t, "'deploy','console'")
	install(t)
	v := &vm{before: func(int, int, string) (runexec.Result, bool, error) {
		return runexec.Result{Stderr: []byte("ssh: connect to host vm port 22: Connection refused"), ExitCode: 255}, true, &runexec.ExitError{Code: 255}
	}}
	started := time.Now()
	out, stderr, err := status(t, dir, true, v)
	var ue *ui.Error
	if !errors.As(err, &ue) || ue.What != "Cannot read console: the service for production did not answer." || ue.Try != "spinup doctor --remote --env production" {
		t.Fatalf("error = %#v", err)
	}
	if out != "" || !strings.Contains(stderr, "Connection refused") {
		t.Fatalf("stdout=%q stderr=%q", out, stderr)
	}
	if len(v.forwards()) != 1 || time.Since(started) > startupBound {
		t.Fatalf("forwards = %d after %s, want one failed attempt without waiting", len(v.forwards()), time.Since(started))
	}
}

func TestConsoleStatusHostKeyDiagnostics(t *testing.T) {
	dir := writeProject(t, "'deploy','console'")
	install(t)
	v := &vm{before: func(int, int, string) (runexec.Result, bool, error) {
		stderr := "@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@\nOffending ED25519 key in /home/op/.ssh/known_hosts:3\nHost key verification failed."
		return runexec.Result{Stderr: []byte(stderr), ExitCode: 255}, true, &runexec.ExitError{Code: 255}
	}}
	out, _, err := status(t, dir, false, v)
	if err == nil || !strings.Contains(err.Error(), "REMOTE HOST IDENTIFICATION HAS CHANGED") || !strings.Contains(err.Error(), "Offending ED25519 key") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("error = %v", err)
	}
	if out != "" {
		t.Fatalf("stdout = %q", out)
	}
}

func TestConsoleStatusUnhealthyService(t *testing.T) {
	dir := writeProject(t, "'deploy','console'")
	install(t)
	startupBound = 300 * time.Millisecond
	svc := &service{overview: goldenOverview, status: http.StatusServiceUnavailable}
	v := &vm{handlers: map[int]http.Handler{consolePort: svc}}
	out, _, err := status(t, dir, true, v)
	var ue *ui.Error
	if !errors.As(err, &ue) || ue.What != "Cannot read console: the service for production did not answer." {
		t.Fatalf("error = %#v", err)
	}
	if out != "" || svc.health.Load() == 0 {
		t.Fatalf("stdout=%q health checks=%d", out, svc.health.Load())
	}
}

// signal is an output that reports its first write.
type signal struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	once  sync.Once
	ready chan struct{}
}

func newSignal() *signal { return &signal{ready: make(chan struct{})} }

func (s *signal) Write(b []byte) (int, error) {
	s.mu.Lock()
	n, err := s.buf.Write(b)
	s.mu.Unlock()
	s.once.Do(func() { close(s.ready) })
	return n, err
}

func (s *signal) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.buf.String() }

type opened struct {
	out    *signal
	errOut *signal
	cancel context.CancelFunc
	done   chan error
}

func startOpen(t *testing.T, dir string, noBrowser bool, v *vm) *opened {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	o := &opened{out: newSignal(), errOut: newSignal(), cancel: cancel, done: make(chan error, 1)}
	go func() {
		o.done <- Open(ctx, Options{Dir: dir, Env: "production", NoBrowser: noBrowser, Out: o.out, ErrOut: o.errOut}, v)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-o.done:
			o.done <- err
		case <-time.After(5 * time.Second):
			t.Error("Open did not return after cancellation")
		}
	})
	return o
}

// url waits for the printed console URL.
func (o *opened) url(t *testing.T) string {
	t.Helper()
	select {
	case <-o.out.ready:
	case err := <-o.done:
		o.done <- err
		t.Fatalf("Open returned before it printed the URL: %v\nstderr: %s", err, o.errOut.String())
	case <-time.After(8 * time.Second):
		t.Fatal("Open printed no URL")
	}
	line, _, _ := strings.Cut(o.out.String(), "\n")
	url, ok := strings.CutPrefix(line, "Console: ")
	if !ok {
		t.Fatalf("stdout = %q", o.out.String())
	}
	return url
}

// stop cancels Open and returns its result.
func (o *opened) stop(t *testing.T) error {
	t.Helper()
	o.cancel()
	select {
	case err := <-o.done:
		o.done <- err
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Open did not return after cancellation")
		return nil
	}
}

func fetch(t *testing.T, url string) string {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	res, err := httpGet(client, url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	return string(body)
}

func TestConsoleOpenWithGrafana(t *testing.T) {
	dir := writeProject(t, "'deploy','console','monitoring'")
	s := install(t)
	svc, g := &service{overview: goldenOverview}, &grafana{}
	v := &vm{handlers: map[int]http.Handler{consolePort: svc, grafanaPort: g}}
	verified := make(chan bool, 1)
	s.browser = func(string) error {
		verified <- svc.health.Load() > 0 && g.health.Load() > 0
		return nil
	}
	o := startOpen(t, dir, false, v)
	url := o.url(t)
	consoleURL, query, ok := strings.Cut(url, "?")
	if !ok || !strings.HasPrefix(consoleURL, "http://127.0.0.1:") || !strings.HasSuffix(consoleURL, "/") || !strings.HasPrefix(query, "grafana_port=") {
		t.Fatalf("URL = %q", url)
	}
	if !<-verified {
		t.Fatal("the browser opened before both health checks passed")
	}
	if got := fetch(t, consoleURL+"healthz"); got != "ok\n" {
		t.Fatalf("console = %q", got)
	}
	gport := strings.TrimPrefix(query, "grafana_port=")
	if got := fetch(t, "http://127.0.0.1:"+gport+"/"); got != "grafana" {
		t.Fatalf("Grafana = %q", got)
	}
	if err := o.stop(t); err != nil {
		t.Fatal(err)
	}
	if v.running.Load() != 0 {
		t.Fatal("a forward still runs after cancellation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.urls) != 1 || s.urls[0] != url || len(s.refresh) == 0 {
		t.Fatalf("browser URLs = %q, refreshes = %d", s.urls, len(s.refresh))
	}
	if want := "Console: " + url + "\nPress Ctrl-C to close the tunnels.\n"; o.out.String() != want {
		t.Fatalf("stdout = %q", o.out.String())
	}
	ports := map[string]bool{}
	for _, f := range v.forwards() {
		for _, p := range []string{":127.0.0.1:7410 ", ":127.0.0.1:3000 "} {
			if strings.Contains(f, p) {
				ports[p] = true
			}
		}
	}
	if len(ports) != 2 {
		t.Fatalf("forwards = %q", v.forwards())
	}
}

func TestConsoleOpenAbsentGrafana(t *testing.T) {
	dir := writeProject(t, "'deploy','console','monitoring'")
	install(t)
	startupBound = 400 * time.Millisecond
	// Nothing listens on VM port 3000, so SSH accepts and closes each connection.
	v := &vm{handlers: map[int]http.Handler{consolePort: &service{overview: goldenOverview}}}
	o := startOpen(t, dir, true, v)
	url := o.url(t)
	if strings.Contains(url, "grafana_port") || !strings.HasSuffix(url, "/") {
		t.Fatalf("URL = %q", url)
	}
	if got := fetch(t, url+"healthz"); got != "ok\n" {
		t.Fatalf("console = %q", got)
	}
	if !strings.Contains(o.errOut.String(), "Grafana did not answer, so the console shows no Grafana links") {
		t.Fatalf("stderr = %q", o.errOut.String())
	}
	// The failed Grafana forward is closed. Only the console forward runs.
	if n := v.running.Load(); n != 1 {
		t.Fatalf("running forwards = %d", n)
	}
	if err := o.stop(t); err != nil {
		t.Fatal(err)
	}
}

func TestConsoleOpenWithoutMonitoringSkipsGrafana(t *testing.T) {
	dir := writeProject(t, "'deploy','console'")
	install(t)
	v := &vm{handlers: map[int]http.Handler{consolePort: &service{overview: goldenOverview}, grafanaPort: &grafana{}}}
	o := startOpen(t, dir, true, v)
	if url := o.url(t); strings.Contains(url, "grafana_port") {
		t.Fatalf("URL = %q", url)
	}
	if forwards := v.forwards(); len(forwards) != 1 {
		t.Fatalf("forwards = %q", forwards)
	}
}

func TestConsoleOpenBrowserFailureKeepsTunnel(t *testing.T) {
	dir := writeProject(t, "'deploy','console'")
	s := install(t)
	failed := make(chan struct{})
	s.browser = func(string) error {
		defer close(failed)
		return errors.New("no display")
	}
	v := &vm{handlers: map[int]http.Handler{consolePort: &service{overview: goldenOverview}}}
	o := startOpen(t, dir, false, v)
	url := o.url(t)
	<-failed
	if got := fetch(t, url+"healthz"); got != "ok\n" {
		t.Fatalf("the tunnel closed after the browser failure: %q", got)
	}
	if err := o.stop(t); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(o.errOut.String(), "Cannot open the browser. Open the printed URL manually.") {
		t.Fatalf("stderr = %q", o.errOut.String())
	}
}

func TestConsoleOpenRetriesOccupiedPort(t *testing.T) {
	dir := writeProject(t, "'deploy','console'")
	install(t)
	var occupied net.Listener
	t.Cleanup(func() {
		if occupied != nil {
			_ = occupied.Close()
		}
	})
	v := &vm{handlers: map[int]http.Handler{consolePort: &service{overview: goldenOverview}}}
	v.before = func(call int, _ int, address string) (runexec.Result, bool, error) {
		if call == 1 {
			// Another process takes the reserved port before SSH binds it.
			l, err := listen(address)
			if err != nil {
				return runexec.Result{}, true, err
			}
			occupied = l
		}
		return runexec.Result{}, false, nil
	}
	o := startOpen(t, dir, true, v)
	url := o.url(t)
	if occupied == nil || strings.Contains(url, ":"+strconv.Itoa(occupied.Addr().(*net.TCPAddr).Port)+"/") {
		t.Fatalf("URL %q uses the occupied port", url)
	}
	if got := fetch(t, url+"healthz"); got != "ok\n" {
		t.Fatalf("console = %q", got)
	}
	if n := len(v.forwards()); n != 2 {
		t.Fatalf("forwards = %d, want one retry", n)
	}
}

func TestConsoleOpenCancellationKeepsOtherForward(t *testing.T) {
	dir := writeProject(t, "'deploy','console'")
	install(t)
	other := &vm{handlers: map[int]http.Handler{consolePort: &service{overview: goldenOverview}}}
	otherPort, closeOther, err := remote.ForwardLoopback(context.Background(), remote.Host{Address: "vm"}, other, remote.Options{}, consolePort)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeOther() }()
	v := &vm{handlers: map[int]http.Handler{consolePort: &service{overview: goldenOverview}}}
	o := startOpen(t, dir, true, v)
	url := o.url(t)
	if err = o.stop(t); err != nil {
		t.Fatal(err)
	}
	if v.running.Load() != 0 {
		t.Fatal("the cancelled console forward still runs")
	}
	if got := fetch(t, "http://127.0.0.1:"+strconv.Itoa(otherPort)+"/healthz"); got != "ok\n" {
		t.Fatalf("the other forward = %q", got)
	}
	client := &http.Client{Timeout: time.Second}
	if res, err := httpGet(client, url+"healthz"); err == nil {
		_ = res.Body.Close()
		t.Fatal("the console forward answers after cancellation")
	}
}

func TestConsoleOpenRefreshesWhileOpen(t *testing.T) {
	dir := writeProject(t, "'deploy','console'")
	s := install(t)
	refreshInterval = 30 * time.Millisecond
	v := &vm{handlers: map[int]http.Handler{consolePort: &service{overview: goldenOverview}}}
	o := startOpen(t, dir, true, v)
	o.url(t)
	deadline := time.Now().Add(5 * time.Second)
	for s.refreshes() < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := s.refreshes(); n < 3 {
		t.Fatalf("refreshes = %d, want the initial check and periodic checks", n)
	}
}

func TestConsoleOpenReportsSSHExitWhileOpen(t *testing.T) {
	dir := writeProject(t, "'deploy','console'")
	install(t)
	aliveInterval = 20 * time.Millisecond
	exit := make(chan struct{})
	v := &vm{}
	v.before = func(_ int, _ int, address string) (runexec.Result, bool, error) {
		l, err := listen(address)
		if err != nil {
			return runexec.Result{}, true, err
		}
		server := &http.Server{ReadHeaderTimeout: time.Second, Handler: &service{}}
		go func() { _ = server.Serve(l) }()
		<-exit
		_ = server.Close()
		return runexec.Result{Stderr: []byte("Connection to vm closed by remote host."), ExitCode: 255}, true, &runexec.ExitError{Code: 255}
	}
	o := startOpen(t, dir, true, v)
	o.url(t)
	close(exit)
	select {
	case err := <-o.done:
		o.done <- err
		var ue *ui.Error
		if !errors.As(err, &ue) || ue.What != "Cannot read console: the service for production did not answer." {
			t.Fatalf("error = %#v", err)
		}
		if !strings.Contains(o.errOut.String(), "Connection to vm closed by remote host.") {
			t.Fatalf("stderr = %q", o.errOut.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Open kept running after SSH exited")
	}
}

func TestConsoleOpenDisabledModule(t *testing.T) {
	dir := writeProject(t, "'deploy'")
	install(t)
	err := Open(context.Background(), Options{Dir: dir, Env: "production"}, &vm{})
	var ue *ui.Error
	if !errors.As(err, &ue) || ue.What != "Cannot open console: module console is disabled." {
		t.Fatalf("error = %#v", err)
	}
}

// listen opens a local TCP listener.
func listen(address string) (net.Listener, error) {
	return (&net.ListenConfig{}).Listen(context.Background(), "tcp4", address)
}

func httpGet(client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

// This history matches TestProjectHistoryLimits's empty and unknown metadata fixture.
func TestValidateProjectHistoryMetadata(t *testing.T) {
	history := `[{"id":"20261122T170000Z-0123456789ab","at":"2026-11-22T17:00:00Z","user":"","git_sha":null,"color":null,"result":"recorded"},{"id":"20261121T170000Z-abcdef012345","at":"2026-11-21T17:00:00Z","user":null,"git_sha":null,"color":null,"result":"recorded"}]`
	top, err := object([]byte(goldenOverview))
	if err != nil {
		t.Fatal(err)
	}
	section, err := object(top["history"])
	if err != nil {
		t.Fatal(err)
	}
	section["data"] = json.RawMessage(history)
	top["history"], err = json.Marshal(section)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(top)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validate(body, "todo", "production"); err != nil {
		t.Fatalf("projector history rejected: %v", err)
	}
	for _, change := range []struct{ from, to string }{
		{`"id":"20261122T170000Z-0123456789ab"`, `"id":""`},
		{`"result":"recorded"`, `"result":""`},
		{`"git_sha":null`, `"git_sha":""`},
		{`"color":null`, `"color":""`},
		{`"name":"todo-caddy-1"`, `"name":""`},
		{`"user":""`, `"user":12`},
	} {
		if _, err := validate([]byte(strings.Replace(string(body), change.from, change.to, 1)), "todo", "production"); err == nil {
			t.Errorf("accepted %s", change.to)
		}
	}
}
