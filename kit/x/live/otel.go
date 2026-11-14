package live

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const instrumentationName = "github.com/stopyellingatme/spinup/kit/x/live"

type metrics struct {
	tracer    trace.Tracer
	active    metric.Int64UpDownCounter
	opened    metric.Int64Counter
	duration  metric.Float64Histogram
	syncs     metric.Int64Counter
	received  metric.Int64Counter
	delivered metric.Int64Counter
	recovered metric.Int64Counter
	rejected  metric.Int64Counter
	snapshot  metric.Float64Histogram
	listener  metric.Int64ObservableGauge
	ring      metric.Int64ObservableGauge
	meter     metric.Meter
}

func newMetrics(mp metric.MeterProvider, tp trace.TracerProvider) (*metrics, error) {
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	meter := mp.Meter(instrumentationName)
	m := &metrics{meter: meter, tracer: tp.Tracer(instrumentationName)}
	var problems []error
	var err error
	m.active, err = meter.Int64UpDownCounter("live.stream.active", metric.WithUnit("{stream}"))
	problems = append(problems, err)
	m.opened, err = meter.Int64Counter("live.stream.opened", metric.WithUnit("{stream}"))
	problems = append(problems, err)
	m.duration, err = meter.Float64Histogram("live.stream.duration", metric.WithUnit("s"))
	problems = append(problems, err)
	m.syncs, err = meter.Int64Counter("live.sync", metric.WithUnit("{event}"))
	problems = append(problems, err)
	m.received, err = meter.Int64Counter("live.patch.received", metric.WithUnit("{patch}"))
	problems = append(problems, err)
	m.delivered, err = meter.Int64Counter("live.patch.delivered", metric.WithUnit("{patch}"))
	problems = append(problems, err)
	m.recovered, err = meter.Int64Counter("live.poll.recovered", metric.WithUnit("{patch}"))
	problems = append(problems, err)
	m.rejected, err = meter.Int64Counter("live.bootstrap.rejected", metric.WithUnit("{request}"))
	problems = append(problems, err)
	m.snapshot, err = meter.Float64Histogram("live.snapshot.duration", metric.WithUnit("s"))
	problems = append(problems, err)
	m.listener, err = meter.Int64ObservableGauge("live.listener.up", metric.WithUnit("{state}"))
	problems = append(problems, err)
	m.ring, err = meter.Int64ObservableGauge("live.ring.bytes", metric.WithUnit("By"))
	problems = append(problems, err)
	if err := errors.Join(problems...); err != nil {
		return nil, &Error{Op: "create metrics", Err: err, Try: "check the OpenTelemetry meter provider"}
	}
	return m, nil
}

func (m *metrics) start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	if m == nil {
		return noop.NewTracerProvider().Tracer(instrumentationName).Start(ctx, name)
	}
	return m.tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindInternal), trace.WithAttributes(attrs...))
}

func feedAttribute(feed string) attribute.KeyValue {
	return attribute.String("live.feed", feedOf(Key(feed)))
}

func (m *metrics) streamOpened(feed, result string) {
	if m == nil {
		return
	}
	m.opened.Add(context.Background(), 1, metric.WithAttributes(feedAttribute(feed), attribute.String("live.result", result)))
}

func (m *metrics) streamActive(feed string, delta int64) {
	if m == nil {
		return
	}
	m.active.Add(context.Background(), delta, metric.WithAttributes(feedAttribute(feed)))
}

func (m *metrics) streamEnded(feed, end string, d time.Duration) {
	if m == nil {
		return
	}
	m.duration.Record(context.Background(), d.Seconds(), metric.WithAttributes(feedAttribute(feed), attribute.String("live.end", end)))
}

func (m *metrics) syncSent(feed, reason string) {
	if m == nil {
		return
	}
	m.syncs.Add(context.Background(), 1, metric.WithAttributes(feedAttribute(feed), attribute.String("live.reason", reason)))
}

func (m *metrics) patchReceived(feed string, n int64) {
	if m == nil {
		return
	}
	m.received.Add(context.Background(), n, metric.WithAttributes(feedAttribute(feed)))
}

func (m *metrics) patchDelivered(feed string, n int64) {
	if m == nil {
		return
	}
	m.delivered.Add(context.Background(), n, metric.WithAttributes(feedAttribute(feed)))
}

func (m *metrics) pollRecovered(n int64) {
	if m == nil {
		return
	}
	m.recovered.Add(context.Background(), n)
}

func (m *metrics) bootstrapRejected() {
	if m == nil {
		return
	}
	m.rejected.Add(context.Background(), 1)
}

func (m *metrics) snapshotDone(feed string, d time.Duration, err error) {
	if m == nil {
		return
	}
	attrs := []attribute.KeyValue{feedAttribute(feed)}
	if err != nil {
		attrs = append(attrs, attribute.String("error.type", fmt.Sprintf("%T", err)))
	}
	m.snapshot.Record(context.Background(), d.Seconds(), metric.WithAttributes(attrs...))
}

func (m *metrics) observe(listenerUp func() bool, ringBytes func() int64) {
	if m == nil {
		return
	}
	_, err := m.meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		var up int64
		if listenerUp != nil && listenerUp() {
			up = 1
		}
		o.ObserveInt64(m.listener, up)
		var bytes int64
		if ringBytes != nil {
			bytes = ringBytes()
		}
		o.ObserveInt64(m.ring, bytes)
		return nil
	}, m.listener, m.ring)
	if err != nil {
		otel.Handle(err)
	}
}
