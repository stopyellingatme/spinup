package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestHTTPMetricCardinality(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	enabled.Store(true)
	t.Cleanup(func() {
		enabled.Store(false)
		otel.SetMeterProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/items/{id}", func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Error("instrumentation lost the Flusher interface")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := HTTPMiddleware("test")(mux)
	for _, method := range []string{"CUSTOM_ONE", "CUSTOM_TWO"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), method, "/items/123", nil))
	}
	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "http.server.request.count" {
				continue
			}
			points := m.Data.(metricdata.Sum[int64]).DataPoints
			if len(points) != 1 || points[0].Value != 2 {
				t.Fatalf("unbounded method series: %v", points)
			}
			method, _ := points[0].Attributes.Value("http.request.method")
			if method.AsString() != "_OTHER" {
				t.Fatal(method)
			}
			return
		}
	}
	t.Fatal("request count is absent")
}
