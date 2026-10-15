package telemetry

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	logpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSetupExportsAndShutdown(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://collector.test/otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-test=present")
	t.Setenv("OTEL_SERVICE_NAME", "environment-api")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.name=attributes-api,team=platform")
	t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_always_on")
	var mu sync.Mutex
	bodies := map[string][]byte{}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		_ = r.Body.Close()
		if r.Header.Get("X-Test") != "present" {
			t.Error("OTLP headers were not applied")
		}
		mu.Lock()
		bodies[r.URL.Path] = append(bodies[r.URL.Path], body...)
		mu.Unlock()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/x-protobuf"}}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})}
	stop, err := setup(context.Background(), Options{ServiceName: "test-api", ServiceVersion: "v1", Environment: "test"}, client)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stop(context.Background()) }()
	if !Enabled() {
		t.Fatal("setup did not enable telemetry")
	}
	ctx, span := Tracer("test").Start(context.Background(), "operation")
	var local bytes.Buffer
	slog.New(SlogHandler(slog.NewJSONHandler(&local, nil))).With("password", "hidden-password").WithGroup("request").InfoContext(ctx, "postgres://app:hidden-url@db/test", slog.Group("nested", "token", "hidden-token"))
	counter, err := Meter("test").Int64Counter("test.requests")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(ctx, 1)
	span.End()
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	if !strings.HasPrefix(carrier["traceparent"], "00-"+span.SpanContext().TraceID().String()) {
		t.Fatal(carrier)
	}
	if err := stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if Enabled() {
		t.Fatal("shutdown left telemetry enabled")
	}
	if err := stop(context.Background()); err != nil {
		t.Fatal("second shutdown:", err)
	}
	mu.Lock()
	defer mu.Unlock()
	var traces tracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(bodies["/otlp/v1/traces"], &traces); err != nil {
		t.Fatal(err)
	}
	if len(traces.ResourceSpans) != 1 || len(traces.ResourceSpans[0].ScopeSpans[0].Spans) != 1 {
		t.Fatalf("traces = %v", &traces)
	}
	attrs := map[string]string{}
	for _, a := range traces.ResourceSpans[0].Resource.Attributes {
		attrs[a.Key] = a.Value.GetStringValue()
	}
	for key, want := range map[string]string{"service.name": "environment-api", "service.version": "v1", "deployment.environment.name": "test", "team": "platform"} {
		if attrs[key] != want {
			t.Errorf("%s = %q, want %q", key, attrs[key], want)
		}
	}
	if attrs["host.name"] == "" {
		t.Error("host.name is absent")
	}
	var metrics metricpb.ExportMetricsServiceRequest
	if err := proto.Unmarshal(bodies["/otlp/v1/metrics"], &metrics); err != nil {
		t.Fatal(err)
	}
	if len(metrics.ResourceMetrics) != 1 {
		t.Fatalf("metrics = %v", &metrics)
	}
	var logs logpb.ExportLogsServiceRequest
	if err := proto.Unmarshal(bodies["/otlp/v1/logs"], &logs); err != nil {
		t.Fatal(err)
	}
	if len(logs.ResourceLogs) != 1 {
		t.Fatalf("logs = %v", &logs)
	}
	records := logs.ResourceLogs[0].ScopeLogs[0].LogRecords
	if len(records) != 1 || len(records[0].TraceId) != 16 || len(records[0].SpanId) != 8 {
		t.Fatalf("records = %v", records)
	}
	for _, data := range []string{local.String(), logs.String()} {
		if strings.Contains(data, "hidden-") || !strings.Contains(data, "****") {
			t.Fatal(data)
		}
	}
}

func TestEnvironmentSampler(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://collector.test")
	t.Setenv("OTEL_TRACES_SAMPLER", "parentbased_traceidratio")
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0")
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_ = r.Body.Close()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/x-protobuf"}}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})}
	stop, err := setup(context.Background(), Options{ServiceName: "test-api"}, client)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stop(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	_, root := Tracer("test").Start(context.Background(), "unsampled-root")
	if root.IsRecording() {
		t.Error("zero ratio sampled a root span")
	}
	root.End()
	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled, Remote: true,
	})
	_, child := Tracer("test").Start(trace.ContextWithRemoteSpanContext(context.Background(), parent), "sampled-child")
	if !child.IsRecording() {
		t.Error("parent sampling was not preserved")
	}
	child.SetAttributes(attribute.String("test", "value"))
	child.End()
}

func TestSetupResourceFailure(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://collector.test")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "invalid")
	previous := otel.GetTracerProvider()
	_, err := Setup(context.Background(), Options{})
	if err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("error = %v", err)
	}
	if Enabled() || previous != otel.GetTracerProvider() {
		t.Fatal("failed setup installed providers")
	}
}
