package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestDisabledSetup(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://collector.test/v1/traces")
	lp := otel.GetLoggerProvider()
	tp, mp, propagator := otel.GetTracerProvider(), otel.GetMeterProvider(), otel.GetTextMapPropagator()
	stop, err := Setup(context.Background(), Options{ServiceName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if Enabled() || lp != otel.GetLoggerProvider() || tp != otel.GetTracerProvider() || mp != otel.GetMeterProvider() || propagator != otel.GetTextMapPropagator() {
		t.Fatal("disabled setup installed telemetry")
	}
	if err := stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := http.NewServeMux()
	if HTTPMiddleware("test")(h) != h {
		t.Fatal("disabled telemetry wrapped the handler")
	}
}

func TestHTTPTracingAndCorrelation(t *testing.T) {
	// Setup is tested separately. This test observes real SDK spans without a listener.
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	old := otel.GetTracerProvider()
	oldProp := otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	enabled.Store(true)
	t.Cleanup(func() {
		enabled.Store(false)
		otel.SetTracerProvider(old)
		otel.SetTextMapPropagator(oldProp)
		_ = provider.Shutdown(context.Background())
	})
	var out bytes.Buffer
	logger := slog.New(SlogHandler(slog.NewJSONHandler(&out, nil))).With("password", "hidden")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(_ http.ResponseWriter, r *http.Request) {
		if baggage.FromContext(r.Context()).Member("tenant").Value() != "test" {
			t.Error("baggage was not extracted")
		}
		logger.InfoContext(r.Context(), "request", slog.Group("nested", "token", "hidden"))
	})
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/items/123", nil)
	request.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	request.Header.Set("baggage", "tenant=test")
	HTTPMiddleware("test")(mux).ServeHTTP(httptest.NewRecorder(), request)
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "GET /items/{id}" {
		t.Fatalf("spans = %+v", spans)
	}
	if spans[0].Parent.SpanID().String() != "0123456789abcdef" || !spans[0].Parent.IsRemote() {
		t.Fatal("remote parent lost")
	}
	var record map[string]any
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["trace_id"] != "0123456789abcdef0123456789abcdef" || record["span_id"] != spans[0].SpanContext.SpanID().String() {
		t.Fatal(record)
	}
	if record["password"] != "****" || record["nested"].(map[string]any)["token"] != "****" {
		t.Fatal(record)
	}
}
