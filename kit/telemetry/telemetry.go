// Package telemetry configures optional OTLP/HTTP export for application signals.
// Call Setup before constructing the application or its database pool.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Options supplies resource defaults. Standard OTel environment values override them.
type Options struct {
	// ServiceName identifies the application, normally its name plus "-api".
	ServiceName string
	// ServiceVersion identifies the application release.
	ServiceVersion string
	// Environment identifies the deployment environment.
	Environment string
}

var enabled atomic.Bool
var setupMu sync.Mutex

// Enabled reports whether Setup installed active telemetry providers.
func Enabled() bool { return enabled.Load() }

// Tracer returns the named tracer from the current provider.
func Tracer(name string) trace.Tracer { return otel.Tracer(name) }

// Meter returns the named meter from the current provider.
func Meter(name string) metric.Meter { return otel.Meter(name) }

// Setup installs traces, metrics, logs, and W3C propagation when an OTLP endpoint exists.
// Without OTEL_EXPORTER_OTLP_ENDPOINT, it changes nothing and returns a no-op shutdown.
// Call the returned shutdown after application work finishes to flush all signals.
func Setup(ctx context.Context, o Options) (func(context.Context) error, error) {
	return setup(ctx, o, nil)
}

// A client allows exporter tests to use an in-memory HTTP transport.
// A nil client preserves all standard exporter environment configuration.
func setup(ctx context.Context, o Options, client *http.Client) (func(context.Context) error, error) {
	setupMu.Lock()
	defer setupMu.Unlock()
	if strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) == "" {
		return func(context.Context) error { return nil }, nil
	}
	if Enabled() {
		return nil, fmt.Errorf("cannot start telemetry: providers are already active\nTry: call telemetry.Setup once before constructing the app")
	}
	res, err := resource.New(ctx, resource.WithAttributes(
		attribute.String("service.name", o.ServiceName),
		attribute.String("service.version", o.ServiceVersion),
		attribute.String("deployment.environment.name", o.Environment),
	), resource.WithHost(), resource.WithFromEnv())
	if err != nil {
		return nil, setupError("resource", err)
	}
	var traceOpts []otlptracehttp.Option
	var metricOpts []otlpmetrichttp.Option
	var logOpts []otlploghttp.Option
	if client != nil {
		traceOpts = append(traceOpts, otlptracehttp.WithHTTPClient(client))
		metricOpts = append(metricOpts, otlpmetrichttp.WithHTTPClient(client))
		logOpts = append(logOpts, otlploghttp.WithHTTPClient(client))
	}
	spans, err := otlptracehttp.New(ctx, traceOpts...)
	if err != nil {
		return nil, setupError("trace exporter", err)
	}
	metrics, err := otlpmetrichttp.New(ctx, metricOpts...)
	if err != nil {
		return nil, setupError("metric exporter", errors.Join(err, spans.Shutdown(ctx)))
	}
	logs, err := otlploghttp.New(ctx, logOpts...)
	if err != nil {
		return nil, setupError("log exporter", errors.Join(err, spans.Shutdown(ctx), metrics.Shutdown(ctx)))
	}
	// The trace SDK reads OTEL_TRACES_SAMPLER and OTEL_TRACES_SAMPLER_ARG.
	// Its default is ParentBased(AlwaysSample).
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(spans))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(
		sdkmetric.NewPeriodicReader(metrics, sdkmetric.WithInterval(30*time.Second)),
	))
	lp := sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewBatchProcessor(logs)))
	// The Go runtime dashboard reads these metrics (goroutines, memory, GC goal, scheduler latency).
	if err := otelruntime.Start(otelruntime.WithMeterProvider(mp)); err != nil {
		return nil, setupError("runtime metrics", errors.Join(err, tp.Shutdown(ctx), mp.Shutdown(ctx), lp.Shutdown(ctx)))
	}
	previousTP, previousMP := otel.GetTracerProvider(), otel.GetMeterProvider()
	previousLP, previousProp := otel.GetLoggerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetLoggerProvider(lp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	enabled.Store(true)
	var once sync.Once
	var shutdownErr error
	return func(ctx context.Context) error {
		once.Do(func() {
			setupMu.Lock()
			defer setupMu.Unlock()
			enabled.Store(false)
			shutdownErr = errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx), lp.Shutdown(ctx))
			otel.SetTracerProvider(previousTP)
			otel.SetMeterProvider(previousMP)
			otel.SetLoggerProvider(previousLP)
			otel.SetTextMapPropagator(previousProp)
			if shutdownErr != nil {
				shutdownErr = fmt.Errorf("cannot shut down telemetry: %w\nTry: check the collector endpoint and shutdown timeout", shutdownErr)
			}
		})
		return shutdownErr
	}, nil
}

func setupError(component string, err error) error {
	return fmt.Errorf("cannot configure telemetry %s: %w\nTry: check the OTEL environment configuration", component, err)
}
