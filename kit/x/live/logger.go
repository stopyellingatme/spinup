package live

import (
	"context"
	"log/slog"
)

// lateDefault is a slog.Handler that sends each record to the handler of slog.Default() at the
// time of the call. The hub is built before app.New installs the logger of the app.
type lateDefault struct{}

func (lateDefault) Enabled(ctx context.Context, level slog.Level) bool {
	return slog.Default().Handler().Enabled(ctx, level)
}

func (lateDefault) Handle(ctx context.Context, record slog.Record) error {
	return slog.Default().Handler().Handle(ctx, record)
}

func (lateDefault) WithAttrs(attrs []slog.Attr) slog.Handler {
	return slog.Default().Handler().WithAttrs(attrs)
}

func (lateDefault) WithGroup(name string) slog.Handler {
	return slog.Default().Handler().WithGroup(name)
}
