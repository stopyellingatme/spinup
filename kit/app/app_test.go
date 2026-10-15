package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/config"
	"github.com/stopyellingatme/spinup/kit/httpx"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestBuiltinRoutes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path string
		want map[string]any
	}{
		{"/healthz", map[string]any{"status": "ok"}},
		{"/readyz", map[string]any{"status": "ok", "checks": map[string]any{}}},
		{"/version", map[string]any{"name": "todo", "version": "1.2.3", "go": runtime.Version()}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			a := New(WithName("todo"), WithVersion("1.2.3"), WithLogger(discardLogger()))
			w := httptest.NewRecorder()
			a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil))
			var got map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if w.Header().Get("X-Request-ID") == "" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal(w.Header())
			}
		})
	}
}

func TestReadiness(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "ok", true: "fail"}[fail], func(t *testing.T) {
			t.Parallel()
			var order []string
			var logs bytes.Buffer
			checkErr := errors.New("database unavailable: user=private-user database=private-db host=private-host port=5432 " + strings.Repeat("details", 40))
			a := New(WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))), WithReadiness("zeta", func(ctx context.Context) error {
				order = append(order, "zeta")
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 2*time.Second {
					t.Error("missing readiness timeout")
				}
				return nil
			}), WithReadiness("alpha", func(context.Context) error {
				order = append(order, "alpha")
				if fail {
					return checkErr
				}
				return nil
			}))
			w := httptest.NewRecorder()
			a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil))
			if len(order) != 0 {
				t.Fatal("healthz checked dependencies")
			}
			w = httptest.NewRecorder()
			a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))
			status := 200
			if fail {
				status = 503
			}
			if w.Code != status || strings.Join(order, ",") != "alpha,zeta" {
				t.Fatalf("%d %v", w.Code, order)
			}
			var body struct {
				Status string            `json:"status"`
				Checks map[string]string `json:"checks"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			want := "ok"
			if fail {
				want = "fail"
			}
			if body.Status != want || !reflect.DeepEqual(body.Checks, map[string]string{"alpha": want, "zeta": "ok"}) {
				t.Fatalf("readiness response = %s", w.Body)
			}
			warnings := 0
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var record map[string]any
				if err := json.Unmarshal([]byte(line), &record); err != nil {
					t.Fatal(err)
				}
				if record["level"] != "WARN" {
					continue
				}
				warnings++
				if record["check"] != "alpha" || record["error"] != checkErr.Error() || record["request_id"] != w.Header().Get("X-Request-ID") {
					t.Fatalf("readiness warning = %v", record)
				}
			}
			wantWarnings := 0
			if fail {
				wantWarnings = 1
			}
			if warnings != wantWarnings {
				t.Fatalf("warnings = %d, want %d", warnings, wantWarnings)
			}
		})
	}
}

func TestRoutesAndMiddleware(t *testing.T) {
	t.Parallel()
	var order []string
	route := func(name string) func(*http.ServeMux) {
		return func(mux *http.ServeMux) {
			order = append(order, name)
			mux.HandleFunc("GET /"+name, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(name)) })
		}
	}
	middleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if httpx.RequestIDFrom(r.Context()) == "" || w.Header().Get("X-Frame-Options") != "DENY" {
				t.Fatal("default middleware order")
			}
			next.ServeHTTP(w, r)
		})
	}
	a := New(WithLogger(discardLogger()), WithRoutes(route("first")), WithRoutes(route("second")), WithMiddleware(middleware))
	for _, path := range []string{"first", "second"} {
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/"+path, nil))
		if w.Body.String() != path {
			t.Fatal(w.Body)
		}
	}
	if strings.Join(order, ",") != "first,second" {
		t.Fatal(order)
	}
}

func TestCommands(t *testing.T) {
	t.Parallel()
	var got []string
	commandErr := errors.New("command failure")
	a := New(WithLogger(discardLogger()), WithCommand("zeta", func(context.Context, []string) error { return nil }), WithCommand("migrate", func(ctx context.Context, args []string) error {
		got = args
		if ctx.Value(commandKey{}) != "value" {
			t.Fatal("context changed")
		}
		return commandErr
	}))
	ctx := context.WithValue(context.Background(), commandKey{}, "value")
	if err := a.Run(ctx, []string{"migrate", "up"}); !errors.Is(err, commandErr) || !reflect.DeepEqual(got, []string{"up"}) {
		t.Fatalf("%v %v", got, err)
	}
	if err := a.Run(ctx, []string{"unknown"}); err == nil || !strings.Contains(err.Error(), "migrate, zeta") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("%v", err)
	}
	if err := New(WithLogger(discardLogger())).Run(ctx, []string{"unknown"}); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("%v", err)
	}
}

type commandKey struct{}

type notifyHandler struct {
	slog.Handler
	started chan struct{}
	once    sync.Once
}

func (h *notifyHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "HTTP server listening" {
		h.once.Do(func() { close(h.started) })
	}
	return h.Handler.Handle(ctx, r)
}

func TestGracefulShutdown(t *testing.T) {
	t.Parallel()
	h := &notifyHandler{Handler: slog.NewTextHandler(io.Discard, nil), started: make(chan struct{})}
	var order []int
	hookErr := errors.New("hook failure")
	a := New(WithLogger(slog.New(h)), WithAddr("127.0.0.1:0"), WithConfig(config.Base{ShutdownTimeout: time.Second}), WithShutdown(func(ctx context.Context) error {
		if ctx.Err() != nil {
			t.Errorf("hook context: %v", ctx.Err())
		}
		order = append(order, 1)
		return nil
	}), WithShutdown(func(context.Context) error { order = append(order, 2); return hookErr }))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, nil) }()
	select {
	case <-h.started:
	case err := <-done:
		t.Fatalf("server did not start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("listen timeout")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, hookErr) || !reflect.DeepEqual(order, []int{2, 1}) {
			t.Fatalf("%v %v", order, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timeout")
	}
}

func TestDefaults(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	a := New(WithConfig(config.Base{Port: 9090}), WithLogger(slog.New(slog.NewJSONHandler(&out, nil))))
	if a.addr != ":9090" || a.config.ShutdownTimeout != 20*time.Second || New(WithLogger(discardLogger())).addr != ":8080" {
		t.Fatal("incorrect defaults")
	}
}

func TestReadinessPanic(t *testing.T) {
	t.Parallel()
	a := New(WithLogger(discardLogger()), WithReadiness("panic", func(context.Context) error { panic("private failure") }))
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))
	if w.Code != 500 || strings.Contains(w.Body.String(), "private failure") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestReadinessTimeout(t *testing.T) {
	t.Parallel()
	returned := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	a := New(WithLogger(discardLogger()), WithReadiness("slow", func(context.Context) error { defer close(returned); <-release; return nil }))
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), `"slow":"fail"`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	select {
	case <-returned:
		t.Fatal("check did not wait for release")
	default:
	}
}

// A closed listener exercises shutdown without requiring sandbox socket access.
type memoryListener struct {
	closed chan struct{}
	once   sync.Once
}

func (l *memoryListener) Accept() (net.Conn, error) { <-l.closed; return nil, net.ErrClosed }
func (l *memoryListener) Close() error              { l.once.Do(func() { close(l.closed) }); return nil }
func (l *memoryListener) Addr() net.Addr            { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

func TestShutdownHooks(t *testing.T) {
	t.Parallel()
	h := &notifyHandler{Handler: slog.NewTextHandler(io.Discard, nil), started: make(chan struct{})}
	listener := &memoryListener{closed: make(chan struct{})}
	var order []int
	hookErr := errors.New("hook failed")
	a := New(WithLogger(slog.New(h)), WithShutdown(func(ctx context.Context) error {
		select {
		case <-listener.closed:
		default:
			t.Error("hook ran before listener closed")
		}
		if ctx.Err() != nil {
			t.Errorf("hook context: %v", ctx.Err())
		}
		order = append(order, 1)
		return nil
	}), WithShutdown(func(context.Context) error { order = append(order, 2); return hookErr }))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.serveListener(ctx, listener) }()
	select {
	case <-h.started:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, hookErr) || !reflect.DeepEqual(order, []int{2, 1}) {
			t.Fatalf("%v %v", order, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timed out")
	}
}

func TestCommandErrorUnchanged(t *testing.T) {
	t.Parallel()
	for _, message := range []string{"command failed", "command failed: invalid argument\nTry: use migrate up"} {
		t.Run(message, func(t *testing.T) {
			t.Parallel()
			commandErr := errors.New(message)
			a := New(WithLogger(discardLogger()), WithCommand("migrate", func(context.Context, []string) error {
				return commandErr
			}))
			err := a.Run(t.Context(), []string{"migrate", "up"})
			if !errors.Is(err, commandErr) || err.Error() != message {
				t.Fatalf("Run error = %v, want unchanged error %q", err, message)
			}
		})
	}
}
