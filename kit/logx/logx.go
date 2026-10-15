package logx

import (
	"context"
	"io"
	"log/slog"
)

// Options configures a logger. Format is "json" (the default) or "text".
// An unrecognized format uses JSON. A nil Level uses slog.LevelInfo.
type Options struct {
	// Level selects the minimum log severity.
	Level slog.Leveler
	// Format selects JSON or text output.
	Format string
	// AddSource includes the source location in each record.
	AddSource bool
}

// New returns a logger that writes to w and uses the current redactor.
func New(w io.Writer, opts Options) *slog.Logger {
	options := &slog.HandlerOptions{Level: opts.Level, AddSource: opts.AddSource}
	var h slog.Handler = slog.NewJSONHandler(w, options)
	if opts.Format == "text" {
		h = slog.NewTextHandler(w, options)
	}
	return slog.New(&redactingHandler{next: h})
}

type loggerKey struct{}

// WithLogger returns a context containing l.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// FromContext returns the context logger, or slog.Default when none is present.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
