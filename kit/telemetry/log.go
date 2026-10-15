package telemetry

import (
	"context"
	"errors"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/trace"

	"github.com/stopyellingatme/spinup/kit/logx"
)

// SlogHandler adds active trace identifiers and redacts records before every sink.
// When telemetry is enabled, it also sends records through the OTel slog bridge.
// The next handler controls the minimum level for both sinks.
func SlogHandler(next slog.Handler) slog.Handler {
	var bridge slog.Handler
	if Enabled() {
		bridge = otelslog.NewHandler("github.com/stopyellingatme/spinup/kit")
	}
	return &correlationHandler{next: logx.RedactHandler(&logHandler{next: next, bridge: bridge})}
}

type correlationHandler struct{ next slog.Handler }

func (h *correlationHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}
func (h *correlationHandler) Handle(ctx context.Context, record slog.Record) error {
	record = record.Clone()
	if span := trace.SpanContextFromContext(ctx); span.IsValid() {
		record.AddAttrs(slog.String("trace_id", span.TraceID().String()), slog.String("span_id", span.SpanID().String()))
	}
	return h.next.Handle(ctx, record)
}
func (h *correlationHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &correlationHandler{next: h.next.WithAttrs(attrs)}
}
func (h *correlationHandler) WithGroup(name string) slog.Handler {
	return &correlationHandler{next: h.next.WithGroup(name)}
}

type logHandler struct{ next, bridge slog.Handler }

func (h *logHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}
func (h *logHandler) Handle(ctx context.Context, record slog.Record) error {
	localErr := h.next.Handle(ctx, record.Clone())
	var bridgeErr error
	if h.bridge != nil && h.bridge.Enabled(ctx, record.Level) {
		bridgeErr = h.bridge.Handle(ctx, record)
	}
	return errors.Join(localErr, bridgeErr)
}
func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var bridge slog.Handler
	if h.bridge != nil {
		bridge = h.bridge.WithAttrs(attrs)
	}
	return &logHandler{next: h.next.WithAttrs(attrs), bridge: bridge}
}
func (h *logHandler) WithGroup(name string) slog.Handler {
	var bridge slog.Handler
	if h.bridge != nil {
		bridge = h.bridge.WithGroup(name)
	}
	return &logHandler{next: h.next.WithGroup(name), bridge: bridge}
}
