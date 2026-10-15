package logx

import (
	"context"
	"log/slog"
	"sync/atomic"
)

// Redactor transforms log attributes. Implementations must support concurrent calls.
// Group members are redacted before the group attribute itself.
type Redactor interface {
	Redact(key string, v slog.Value) slog.Value
}

type redactorState struct{ r Redactor }

var currentRedactor atomic.Pointer[redactorState]

// SetRedactor replaces the redactor for all loggers from New, including existing loggers.
// A nil redactor restores the default, which leaves attributes unchanged.
// SetRedactor is safe for concurrent use.
func SetRedactor(r Redactor) { currentRedactor.Store(&redactorState{r: r}) }

type handlerStep struct {
	group string
	attrs []slog.Attr
}
type redactingHandler struct {
	next  slog.Handler
	steps []handlerStep
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	var r Redactor
	if state := currentRedactor.Load(); state != nil {
		r = state.r
	}
	next := h.next
	for _, step := range h.steps {
		if step.group != "" {
			next = next.WithGroup(step.group)
		} else {
			next = next.WithAttrs(redactAttrs(step.attrs, r))
		}
	}
	clean := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attr slog.Attr) bool { clean.AddAttrs(redactAttr(attr, r)); return true })
	return next.Handle(ctx, clean)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h.withStep(handlerStep{attrs: append([]slog.Attr(nil), attrs...)})
}
func (h *redactingHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.withStep(handlerStep{group: name})
}
func (h *redactingHandler) withStep(step handlerStep) slog.Handler {
	steps := make([]handlerStep, len(h.steps), len(h.steps)+1)
	copy(steps, h.steps)
	return &redactingHandler{next: h.next, steps: append(steps, step)}
}
func redactAttrs(attrs []slog.Attr, r Redactor) []slog.Attr {
	clean := make([]slog.Attr, len(attrs))
	for i, attr := range attrs {
		clean[i] = redactAttr(attr, r)
	}
	return clean
}
func redactAttr(attr slog.Attr, r Redactor) slog.Attr {
	attr.Value = attr.Value.Resolve()
	if attr.Value.Kind() == slog.KindGroup {
		attr.Value = slog.GroupValue(redactAttrs(attr.Value.Group(), r)...)
	}
	if r != nil {
		attr.Value = r.Redact(attr.Key, attr.Value)
	}
	return attr
}
