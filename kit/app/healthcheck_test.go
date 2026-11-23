package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/telemetry"
)

func TestHealthcheckCommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		ok     bool
	}{{"healthy", http.StatusOK, true}, {"unhealthy", http.StatusServiceUnavailable, false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Skipf("listener unavailable: %v", err)
			}
			server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/healthz" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tc.status)
			})}}
			server.Start()
			defer server.Close()
			_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
			a := New(WithLogger(discardLogger()), WithAddr(":"+port))
			if err := a.Run(context.Background(), []string{"healthcheck"}); (err == nil) != tc.ok {
				t.Fatalf("healthcheck error = %v, want ok %v", err, tc.ok)
			}
		})
	}
	a := New(WithLogger(discardLogger()), WithAddr("127.0.0.1:1"))
	if err := a.Run(context.Background(), []string{"healthcheck"}); err == nil {
		t.Fatal("healthcheck passed with no server")
	}
}

// TestHealthcheckSkipsTelemetry runs the path of a generated main.go: telemetry.Setup, then
// Run with "healthcheck", then the telemetry shutdown. The collector accepts connections and
// never answers. The Docker limit is 3 seconds, so the path must end within 2 (finding 29).
// It changes os.Args, so it must not run in parallel.
func TestHealthcheckSkipsTelemetry(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("listener unavailable: %v", err)
	}
	var connections atomic.Int64
	var open []net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			open = append(open, conn)
		}
	}()
	defer func() {
		_ = listener.Close()
		<-done
		for _, conn := range open {
			_ = conn.Close()
		}
	}()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+listener.Addr().String())
	previous := os.Args
	os.Args = []string{"api", "healthcheck"}
	defer func() { os.Args = previous }()

	start := time.Now()
	err = func() (err error) {
		shutdown, err := telemetry.Setup(context.Background(), telemetry.Options{ServiceName: "test-api"})
		if err != nil {
			return err
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = errors.Join(err, shutdown(ctx))
		}()
		a := New(WithLogger(discardLogger()), WithAddr(":"+port))
		return a.Run(context.Background(), os.Args[1:])
	}()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("healthcheck: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("the healthcheck path took %v, want at most 2s", elapsed)
	}
	if n := connections.Load(); n != 0 {
		t.Errorf("the healthcheck path opened %d connections to the collector, want 0", n)
	}
}
