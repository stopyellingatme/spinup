package live

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestMetrics(t *testing.T) {
	ctx := t.Context()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()); _ = tp.Shutdown(context.Background()) })
	m, err := newMetrics(mp, tp)
	if err != nil {
		t.Fatal(err)
	}
	m.streamOpened("notes:42", "ok")
	m.streamActive("notes:42", 2)
	m.streamActive("notes:42", -1)
	m.streamEnded("notes:42", "age", 2*time.Second)
	m.syncSent("notes:42", "gap")
	m.patchReceived("notes:42", 3)
	m.patchDelivered("notes:42", 4)
	m.pollRecovered(5)
	m.bootstrapRejected()
	m.snapshotDone("notes:42", time.Second, nil)
	m.snapshotDone("notes:42", 2*time.Second, errors.New("unavailable"))
	up := true
	m.observe(func() bool { return up }, func() int64 { return 512 })
	expected := map[string]struct {
		unit, kind string
		keys       []string
		value      float64
	}{
		"live.stream.active":      {"{stream}", "sum", []string{"live.feed"}, 1},
		"live.stream.opened":      {"{stream}", "sum", []string{"live.feed", "live.result"}, 1},
		"live.stream.duration":    {"s", "hist", []string{"live.end", "live.feed"}, 2},
		"live.sync":               {"{event}", "sum", []string{"live.feed", "live.reason"}, 1},
		"live.patch.received":     {"{patch}", "sum", []string{"live.feed"}, 3},
		"live.patch.delivered":    {"{patch}", "sum", []string{"live.feed"}, 4},
		"live.poll.recovered":     {"{patch}", "sum", nil, 5},
		"live.bootstrap.rejected": {"{request}", "sum", nil, 1},
		"live.snapshot.duration":  {"s", "hist", []string{"live.feed"}, 3},
		"live.listener.up":        {"{state}", "gauge", nil, 1},
		"live.ring.bytes":         {"By", "gauge", nil, 512},
	}
	var data metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &data); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, scope := range data.ScopeMetrics {
		if scope.Scope.Name != instrumentationName {
			t.Fatalf("scope: %s", scope.Scope.Name)
		}
		for _, metric := range scope.Metrics {
			want, ok := expected[metric.Name]
			if !ok || metric.Unit != want.unit {
				t.Fatalf("metric: %+v", metric)
			}
			seen[metric.Name] = true
			var attrs []attribute.Set
			var value float64
			switch d := metric.Data.(type) {
			case metricdata.Sum[int64]:
				if want.kind != "sum" || d.IsMonotonic != (metric.Name != "live.stream.active") {
					t.Fatalf("sum: %+v", metric)
				}
				for _, p := range d.DataPoints {
					attrs = append(attrs, p.Attributes)
					value += float64(p.Value)
				}
			case metricdata.Histogram[float64]:
				if want.kind != "hist" {
					t.Fatal(metric)
				}
				for _, p := range d.DataPoints {
					attrs = append(attrs, p.Attributes)
					value += p.Sum
					if p.Count != 1 {
						t.Fatal(p)
					}
				}
			case metricdata.Gauge[int64]:
				if want.kind != "gauge" {
					t.Fatal(metric)
				}
				for _, p := range d.DataPoints {
					attrs = append(attrs, p.Attributes)
					value += float64(p.Value)
				}
			default:
				t.Fatalf("metric data: %T", metric.Data)
			}
			if value != want.value || len(attrs) == 0 {
				t.Fatalf("%s value %g, want %g", metric.Name, value, want.value)
			}
			failures := 0
			for _, set := range attrs {
				var keys []string
				for _, a := range set.ToSlice() {
					keys = append(keys, string(a.Key))
					if a.Key == "live.feed" && a.Value.AsString() != "notes" {
						t.Fatal(a)
					}
					switch a.Key {
					case "live.result":
						if a.Value.AsString() != "ok" {
							t.Fatal(a)
						}
					case "live.end":
						if a.Value.AsString() != "age" {
							t.Fatal(a)
						}
					case "live.reason":
						if a.Value.AsString() != "gap" {
							t.Fatal(a)
						}
					case "error.type":
						if a.Value.AsString() == "" {
							t.Fatal(a)
						}
						failures++
					}
				}
				if metric.Name == "live.snapshot.duration" && slices.Contains(keys, "error.type") {
					keys = slices.DeleteFunc(keys, func(k string) bool { return k == "error.type" })
				}
				slices.Sort(keys)
				if !reflect.DeepEqual(keys, want.keys) {
					t.Fatalf("%s keys: %v, want %v", metric.Name, keys, want.keys)
				}
			}
			if metric.Name == "live.snapshot.duration" && (len(attrs) != 2 || failures != 1) {
				t.Fatal("snapshot failure attributes missing")
			}
		}
	}
	if len(seen) != len(expected) {
		t.Fatalf("metrics: %v", seen)
	}
	up = false
	if err := reader.Collect(ctx, &data); err != nil {
		t.Fatal(err)
	}
	for _, scope := range data.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name == "live.listener.up" && metric.Data.(metricdata.Gauge[int64]).DataPoints[0].Value != 0 {
				t.Fatal("listener stayed up")
			}
		}
	}
}

func TestMetricsSpans(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	m, err := newMetrics(nil, tp)
	if err != nil {
		t.Fatal(err)
	}
	ctx, parent := tp.Tracer("test").Start(t.Context(), "request")
	for _, name := range []string{"live.publish", "live.snapshot", "live.load"} {
		_, span := m.start(ctx, name, attribute.String("live.feed", "notes"))
		span.End()
	}
	parent.End()
	spans := exporter.GetSpans()
	if len(spans) != 4 {
		t.Fatal(spans)
	}
	for _, span := range spans[:3] {
		set := attribute.NewSet(span.Attributes...)
		feed, _ := set.Value("live.feed")
		if span.SpanKind != trace.SpanKindInternal || span.Parent.SpanID() != parent.SpanContext().SpanID() || feed.AsString() != "notes" || span.InstrumentationScope.Name != instrumentationName {
			t.Fatal(span)
		}
	}
}

func TestMetricsNil(t *testing.T) {
	var m *metrics
	_, span := m.start(t.Context(), "live.load")
	span.End()
	m.streamOpened("notes", "ok")
	m.streamActive("notes", 1)
	m.streamEnded("notes", "age", time.Second)
	m.syncSent("notes", "gap")
	m.patchReceived("notes", 1)
	m.patchDelivered("notes", 1)
	m.pollRecovered(1)
	m.bootstrapRejected()
	m.snapshotDone("notes", time.Second, nil)
	m.observe(nil, nil)
	if _, err := newMetrics(nil, nil); err != nil {
		t.Fatal(err)
	}
}
