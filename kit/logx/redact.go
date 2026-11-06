package logx

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"
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
		return slog.AnyValue(redactValue(reflect.ValueOf(value.Any()), 0))
	}
	return value
}

func isSecretKey(key string) bool {
	// Most attribute names are already lowercase ASCII.
	folded := false
	for i := 0; i < len(key); i++ {
		if key[i] >= 'A' && key[i] <= 'Z' || key[i] >= utf8.RuneSelf {
			folded = true
			break
		}
	}
	for _, secret := range []string{"password", "secret", "token", "authorization", "cookie", "api_key", "dsn"} {
		if !folded {
			if strings.Contains(key, secret) {
				return true
			}
			continue
		}
		for i := range key {
			if hasLowerPrefix(key[i:], secret) {
				return true
			}
		}
	}
	return false
}

// hasLowerPrefix compares without allocating a lowercase copy. Unicode folding
// matches strings.ToLower, including keys that contain the Kelvin sign.
func hasLowerPrefix(value, lower string) bool {
	for i := 0; i < len(lower); i++ {
		if len(value) == 0 {
			return false
		}
		c, size := rune(value[0]), 1
		if c >= utf8.RuneSelf {
			c, size = utf8.DecodeRuneInString(value)
			c = unicode.ToLower(c)
		} else if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != rune(lower[i]) {
			return false
		}
		value = value[size:]
	}
	return true
}

// Stop before following a container at this depth. This also bounds cycles.
const maxRedactionDepth = 8

func redactValue(value reflect.Value, depth int) any {
	if !value.IsValid() {
		return nil
	}
	if depth >= maxRedactionDepth {
		return "****"
	}
	if value.Kind() == reflect.Interface {
		return redactValue(value.Elem(), depth)
	}
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return nil
	}
	if value.CanInterface() {
		// Errors and Stringers often contain connection URLs.
		switch v := value.Interface().(type) {
		case error:
			return maskURLs(v.Error())
		case fmt.Stringer:
			return maskURLs(v.String())
		}
	}
	switch value.Kind() {
	case reflect.Pointer:
		return redactValue(value.Elem(), depth+1)
	case reflect.String:
		return maskURLs(value.String())
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		if value.Type().Key().Kind() != reflect.String {
			return "****"
		}
		clean := make(map[string]any, value.Len())
		iter := value.MapRange()
		for iter.Next() {
			key := iter.Key().String()
			if isSecretKey(key) {
				clean[key] = "****"
			} else {
				clean[key] = redactValue(iter.Value(), depth+1)
			}
		}
		return clean
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return nil
		}
		// Keep the JSON encoding of byte slices unchanged.
		if value.Type() == reflect.TypeFor[[]byte]() {
			return append([]byte(nil), value.Bytes()...)
		}
		clean := make([]any, value.Len())
		for i := range value.Len() {
			clean[i] = redactValue(value.Index(i), depth+1)
		}
		return clean
	case reflect.Struct:
		clean := make(map[string]any)
		for i := range value.NumField() {
			field := value.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			if isSecretKey(name) || isSecretKey(field.Name) {
				clean[name] = "****"
			} else {
				clean[name] = redactValue(value.Field(i), depth+1)
			}
		}
		return clean
	default:
		return value.Interface()
	}
}

var urlCredentials = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^/@\s:]*:)[^\s]*@`)

func maskURLs(value string) string {
	if !strings.Contains(value, "://") {
		return value
	}
	return urlCredentials.ReplaceAllString(value, "${1}****@")
}
