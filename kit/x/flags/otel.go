package flags

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// EventName is the name of the span event of one evaluation.
const EventName = "feature_flag.evaluation"

// addEvent adds the span event of ADR 0029 decision 12 to the span in ctx.
// It does nothing when the span is not recording, so it makes no
// allocation without telemetry. The event carries no user ID.
func addEvent(ctx context.Context, key string, r Resolution) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.AddEvent(EventName, trace.WithAttributes(
		attribute.String("feature_flag.key", key),
		attribute.String("feature_flag.provider.name", ProviderName),
		attribute.Bool("feature_flag.result.value", r.Value),
		attribute.String("feature_flag.result.variant", r.Variant),
		attribute.String("feature_flag.result.reason", string(r.Reason)),
	))
}
