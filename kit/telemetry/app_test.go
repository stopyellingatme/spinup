package telemetry_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	lognoop "go.opentelemetry.io/otel/log/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/stopyellingatme/spinup/kit/app"
	"github.com/stopyellingatme/spinup/kit/logx"
	"github.com/stopyellingatme/spinup/kit/telemetry"
)

func TestTelemetryRequest(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
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
	// Replace export destinations before any records exist. No network is used.
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetLoggerProvider(lognoop.NewLoggerProvider())
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()); _ = mp.Shutdown(context.Background()) })
	var out bytes.Buffer
	a := app.New(app.WithLogger(logx.New(&out, logx.Options{})), app.WithRoutes(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) {
			var live metricdata.ResourceMetrics
			if err := reader.Collect(r.Context(), &live); err != nil {
				t.Fatal(err)
			}
			var inFlight int64
			for _, scope := range live.ScopeMetrics {
				for _, m := range scope.Metrics {
					if m.Name == "http.server.active_requests" {
						for _, point := range m.Data.(metricdata.Sum[int64]).DataPoints {
							inFlight += point.Value
						}
					}
				}
			}
			if inFlight != 1 {
				t.Errorf("in flight = %d, want 1", inFlight)
			}
			logx.FromContext(r.Context()).InfoContext(r.Context(), "inside", slog.String("secret", "hidden"))
			w.WriteHeader(http.StatusServiceUnavailable)
		})
	}))
	response := httptest.NewRecorder()
	a.Handler().ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/items/123", nil))
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "GET /items/{id}" {
		t.Fatalf("spans = %+v", spans)
	}
	if strings.Count(out.String(), `"trace_id"`) != 2 || strings.Contains(out.String(), "hidden") {
		t.Fatal(out.String())
	}
	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			switch m.Name {
			case "http.server.request.count":
				points := m.Data.(metricdata.Sum[int64]).DataPoints
				if len(points) != 1 || points[0].Value != 1 {
					t.Fatalf("count = %+v", points)
				}
				route, _ := points[0].Attributes.Value("http.route")
				status, _ := points[0].Attributes.Value("http.response.status_code")
				if route.AsString() != "/items/{id}" || status.AsInt64() != 503 {
					t.Fatalf("attributes = %v", points[0].Attributes)
				}
				seen[m.Name] = true
			case "http.server.active_requests":
				points := m.Data.(metricdata.Sum[int64]).DataPoints
				if len(points) != 1 || points[0].Value != 0 {
					t.Fatalf("active = %+v", points)
				}
				seen[m.Name] = true
			case "http.server.request.duration":
				points := m.Data.(metricdata.Histogram[float64]).DataPoints
				if len(points) != 1 || points[0].Count != 1 {
					t.Fatalf("duration = %+v", points)
				}
				route, _ := points[0].Attributes.Value("http.route")
				if route.AsString() != "/items/{id}" {
					t.Fatalf("duration route = %s", route.AsString())
				}
				seen[m.Name] = true
			}
		}
	}
	if len(seen) != 3 {
		t.Fatalf("missing RED metrics: %v", seen)
	}
}
