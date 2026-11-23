package telemetry

import (
	"context"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// silentCollector accepts OTLP connections and never answers. It counts each connection.
func silentCollector(t *testing.T) (endpoint string, connections *atomic.Int64) {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("listener unavailable: %v", err)
	}
	connections = new(atomic.Int64)
	var mu sync.Mutex
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
			mu.Lock()
			open = append(open, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range open {
			_ = conn.Close()
		}
	})
	return "http://" + listener.Addr().String(), connections
}

// withArgs sets the process arguments. Tests that call it must not run in parallel.
func withArgs(t *testing.T, args ...string) {
	t.Helper()
	previous := os.Args
	os.Args = args
	t.Cleanup(func() { os.Args = previous })
}

func TestIsHealthCheck(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{}, false},
		{[]string{"healthcheck"}, true},
		{[]string{"healthcheck", "extra"}, true},
		{[]string{"migrate"}, false},
		{[]string{"user", "healthcheck"}, false},
	} {
		if got := IsHealthCheck(tc.args); got != tc.want {
			t.Errorf("IsHealthCheck(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

// The Docker health check of a generated app runs "api healthcheck" with a 3 second limit.
// Setup plus shutdown must not wait for a collector that does not answer (finding 29).
func TestSetupSkipsHealthCheckCommand(t *testing.T) {
	endpoint, connections := silentCollector(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)
	withArgs(t, "api", HealthCheckCommand)
	start := time.Now()
	stop, err := Setup(context.Background(), Options{ServiceName: "test-api"})
	if err != nil {
		t.Fatal(err)
	}
	if Enabled() {
		t.Error("the health check command started telemetry")
	}
	counter, err := Meter("test").Int64Counter("test.healthchecks")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(context.Background(), 1)
	_, span := Tracer("test").Start(context.Background(), "healthcheck")
	span.End()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := stop(shutdownCtx); err != nil {
		t.Errorf("shutdown: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("setup plus shutdown took %v, want at most 2s", elapsed)
	}
	if n := connections.Load(); n != 0 {
		t.Errorf("the health check command opened %d connections to the collector, want 0", n)
	}
}

// Other commands and the server keep telemetry.
func TestSetupKeepsOtherCommands(t *testing.T) {
	endpoint, _ := silentCollector(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)
	for _, args := range [][]string{{"api"}, {"api", "migrate"}} {
		withArgs(t, args...)
		stop, err := Setup(context.Background(), Options{ServiceName: "test-api"})
		if err != nil {
			t.Fatal(err)
		}
		if !Enabled() {
			t.Errorf("args %q did not start telemetry", args)
		}
		// The collector never answers, so a short deadline ends the flush. Its error is expected.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_ = stop(shutdownCtx)
		cancel()
		if Enabled() {
			t.Fatal("shutdown left telemetry enabled")
		}
	}
}
