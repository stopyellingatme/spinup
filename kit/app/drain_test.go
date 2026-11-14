package app

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/config"
)

// recordingListener records the first Close, which marks the server stop.
type recordingListener struct {
	net.Listener
	once    sync.Once
	onClose func()
}

func (l *recordingListener) Close() error {
	l.once.Do(l.onClose)
	return l.Listener.Close()
}

func loopbackListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener is not available: %v", err)
	}
	return listener
}

// startServe runs serveListener in a goroutine and returns its cancel function and result.
func startServe(a *App, listener net.Listener) (context.CancelFunc, <-chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.serveListener(ctx, listener) }()
	return cancel, done
}

func TestDrainRunsBeforeShutdown(t *testing.T) {
	listener := loopbackListener(t)
	release := make(chan struct{})
	started := make(chan struct{})
	a := New(WithLogger(discardLogger()), WithConfig(config.Base{ShutdownTimeout: 20 * time.Second}),
		WithRequestTimeoutExempt(func(r *http.Request) bool { return r.URL.Path == "/stream" }),
		WithRoutes(func(mux *http.ServeMux) {
			mux.HandleFunc("GET /stream", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "data: open\n\n")
				_ = http.NewResponseController(w).Flush()
				close(started)
				<-release
				_, _ = io.WriteString(w, "data: end\n\n")
			})
		}),
		WithDrain(func(context.Context) error { close(release); return nil }))
	cancel, done := startServe(a, listener)
	defer cancel()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+listener.Addr().String()+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	<-started
	start := time.Now()
	cancel()
	body, readErr := io.ReadAll(response.Body)
	if err := <-done; err != nil {
		t.Fatalf("serveListener failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("serveListener returned after %s, want less than 2s", elapsed)
	}
	if readErr != nil || string(body) != "data: open\n\ndata: end\n\n" {
		t.Fatalf("stream body=%q error=%v", body, readErr)
	}
}

func TestDrainOrderAndDeadline(t *testing.T) {
	for _, tc := range []struct {
		name     string
		shutdown time.Duration
		limit    time.Duration
	}{
		{name: "drain timeout", shutdown: 20 * time.Second, limit: DrainTimeout},
		{name: "shutdown timeout", shutdown: time.Second, limit: time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			var contexts []context.Context
			hook := func(name string) func(context.Context) error {
				return func(ctx context.Context) error {
					order = append(order, name)
					contexts = append(contexts, ctx)
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) > tc.limit {
						t.Errorf("hook %s: deadline=%v ok=%v, want at most %s away", name, deadline, ok, tc.limit)
					}
					return errors.New(name + " failed")
				}
			}
			a := New(WithLogger(discardLogger()), WithConfig(config.Base{ShutdownTimeout: tc.shutdown}),
				WithDrain(hook("first")), WithDrain(nil), WithDrain(hook("second")))
			cancel, done := startServe(a, loopbackListener(t))
			cancel()
			err := <-done
			if strings.Join(order, ",") != "second,first" {
				t.Fatalf("drain order = %v, want second,first", order)
			}
			if len(contexts) != 2 || contexts[0] != contexts[1] {
				t.Fatal("drain hooks did not share one context")
			}
			if err == nil || strings.Count(err.Error(), "drain hook failed") != 2 ||
				!strings.Contains(err.Error(), "first failed") || !strings.Contains(err.Error(), "second failed") {
				t.Fatalf("serveListener error = %v", err)
			}
		})
	}
}

func TestShutdownHooksStillRunAfterDrain(t *testing.T) {
	var mu sync.Mutex
	var order []string
	add := func(step string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, step)
	}
	listener := &recordingListener{Listener: loopbackListener(t), onClose: func() { add("server stop") }}
	a := New(WithLogger(discardLogger()),
		WithShutdown(func(context.Context) error { add("shutdown"); return nil }),
		WithDrain(func(context.Context) error { add("drain"); return nil }))
	cancel, done := startServe(a, listener)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(order, ","); got != "drain,server stop,shutdown" {
		t.Fatalf("order = %s, want drain,server stop,shutdown", got)
	}
}
