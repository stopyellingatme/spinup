package app

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/kit/logx"
	"github.com/stopyellingatme/spinup/kit/telemetry"
)

func TestTelemetryDisabled(t *testing.T) {
	var out bytes.Buffer
	logger := logx.New(&out, logx.Options{})
	a := New(WithLogger(logger))
	if a.logger != logger {
		t.Fatal("disabled telemetry changed the logger")
	}
	a.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/healthz", nil))
	if strings.Contains(out.String(), "trace_id") {
		t.Fatal(out.String())
	}
}

func TestTelemetryLogger(t *testing.T) {
	for _, on := range []bool{false, true} {
		name, endpoint := "disabled", ""
		if on {
			name, endpoint = "enabled", "http://collector.test"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)
			stop, err := telemetry.Setup(context.Background(), telemetry.Options{ServiceName: "test-api"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if err := stop(ctx); err != nil && !errors.Is(err, context.Canceled) {
					t.Error(err)
				}
			})
			var out bytes.Buffer
			logger := logx.New(&out, logx.Options{})
			a := New(WithLogger(logger))
			if changed := a.logger != logger; changed != on {
				t.Fatalf("logger wrapped = %t, want %t", changed, on)
			}
		})
	}
}
