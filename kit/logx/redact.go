package logx

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
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
// A nil redactor restores the default secret masking.
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
	if r == nil {
		r = defaultRedactor{}
	}
	next := h.next
	for _, step := range h.steps {
		if step.group != "" {
			next = next.WithGroup(step.group)
		} else {
			next = next.WithAttrs(redactAttrs(step.attrs, r))
		}
	}
	clean := slog.NewRecord(record.Time, record.Level, maskURLs(record.Message), record.PC)
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

// RedactHandler applies the current redactor before next receives a record.
// It also redacts attributes attached through WithAttrs and WithGroup.
func RedactHandler(next slog.Handler) slog.Handler {
	return &redactingHandler{next: next}
}

type defaultRedactor struct{}

func init() { SetRedactor(defaultRedactor{}) }

func (defaultRedactor) Redact(key string, value slog.Value) slog.Value {
	if isSecretKey(key) {
		return slog.StringValue("****")
	}
	switch value.Kind() {
	case slog.KindString:
		return slog.StringValue(maskURLs(value.String()))
	case slog.KindAny:
		// Errors and Stringers often contain connection URLs.
		switch v := value.Any().(type) {
		case error:
			return slog.StringValue(maskURLs(v.Error()))
		case fmt.Stringer:
			return slog.StringValue(maskURLs(v.String()))
		case http.Header:
			return slog.AnyValue(redactStringSlices(v))
		case url.Values:
			return slog.AnyValue(redactStringSlices(v))
		}
		if redacted, ok := redactStringMap(value.Any()); ok {
			return slog.AnyValue(redacted)
		}
	}
	return value
}

func isSecretKey(key string) bool {
	lower := strings.ToLower(key)
	for _, secret := range []string{"password", "secret", "token", "authorization", "cookie", "api_key", "dsn"} {
		if strings.Contains(lower, secret) {
			return true
		}
	}
	return false
}

func redactStringSlices(values map[string][]string) map[string][]string {
	redacted := make(map[string][]string, len(values))
	for key, value := range values {
		if isSecretKey(key) {
			redacted[key] = []string{"****"}
			continue
		}
		redacted[key] = append([]string(nil), value...)
	}
	return redacted
}

func redactStringMap(value any) (map[string]any, bool) {
	mapValue := reflect.ValueOf(value)
	if mapValue.Kind() != reflect.Map || mapValue.Type().Key().Kind() != reflect.String {
		return nil, false
	}
	redacted := make(map[string]any, mapValue.Len())
	iter := mapValue.MapRange()
	for iter.Next() {
		key := iter.Key().String()
		if isSecretKey(key) {
			redacted[key] = "****"
			continue
		}
		redacted[key] = iter.Value().Interface()
	}
	return redacted, true
}

var urlCredentials = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^/@\s:]*:)[^\s]*@`)

func maskURLs(value string) string { return urlCredentials.ReplaceAllString(value, "${1}****@") }
