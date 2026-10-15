package telemetry

import (
	"net/http"
	"strings"

	"github.com/felixge/httpsnoop"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// HTTPMiddleware traces requests and records their count, duration, and active count.
// Span names and metric labels use route patterns, never concrete request paths.
// A ServeMux can be wrapped directly. Other routers must populate Request.Pattern.
// When telemetry is disabled, it returns the original handler.
func HTTPMiddleware(name string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !Enabled() {
			return next
		}
		meter := Meter("github.com/stopyellingatme/spinup/kit/app")
		count, _ := meter.Int64Counter("http.server.request.count", metric.WithUnit("{request}"))
		active, _ := meter.Int64UpDownCounter("http.server.active_requests", metric.WithUnit("{request}"))
		// otelhttp supplies the standard http.server.request.duration histogram.
		traced := otelhttp.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attrs := metric.WithAttributes(attribute.String("http.request.method", metricMethod(r.Method)))
			active.Add(r.Context(), 1, attrs)
			defer active.Add(r.Context(), -1, attrs)
			result := httpsnoop.CaptureMetrics(next, w, r)
			// Match otelhttp's http.route label, which excludes the method and host.
			route := ""
			if index := strings.IndexByte(r.Pattern, '/'); index >= 0 {
				route = r.Pattern[index:]
			}
			count.Add(r.Context(), 1, metric.WithAttributes(
				attribute.String("http.request.method", metricMethod(r.Method)),
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", result.Code),
			))
		}), name, otelhttp.WithSpanNameFormatter(func(fallback string, r *http.Request) string {
			if r.Pattern != "" {
				return r.Pattern
			}
			return fallback
		}))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if mux, ok := next.(*http.ServeMux); ok {
				_, r.Pattern = mux.Handler(r)
			}
			traced.ServeHTTP(w, r)
		})
	}
}

func metricMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete,
		http.MethodConnect, http.MethodOptions, http.MethodTrace, http.MethodPatch:
		return method
	default:
		return "_OTHER"
	}
}
