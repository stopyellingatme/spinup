//go:build deps

package deps

// M6 pre-seed.
import (
	_ "github.com/exaring/otelpgx"
	_ "go.opentelemetry.io/contrib/bridges/otelslog"
	_ "go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	_ "go.opentelemetry.io/contrib/instrumentation/runtime"
	_ "go.opentelemetry.io/otel"
	_ "go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	_ "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	_ "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	_ "go.opentelemetry.io/otel/propagation"
	_ "go.opentelemetry.io/otel/sdk/log"
	_ "go.opentelemetry.io/otel/sdk/metric"
	_ "go.opentelemetry.io/otel/sdk/resource"
	_ "go.opentelemetry.io/otel/sdk/trace"
)
