package logx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

type secretRedactor struct{}

func (secretRedactor) Redact(key string, v slog.Value) slog.Value {
	if key == "secret" {
		return slog.StringValue("[redacted]")
	}
	return v
}

func TestRedactor(t *testing.T) {
	defer SetRedactor(nil)
	var out bytes.Buffer
	l := New(&out, Options{}).With(slog.Group("saved", slog.String("secret", "before"))).WithGroup("request")
	SetRedactor(secretRedactor{})
	l.Info("test", slog.Group("nested", slog.String("secret", "after")))
	if strings.Contains(out.String(), "before") || strings.Contains(out.String(), "after") {
		t.Fatal(out.String())
	}
	var record map[string]any
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["saved"].(map[string]any)["secret"] != "[redacted]" || record["request"].(map[string]any)["nested"].(map[string]any)["secret"] != "[redacted]" {
		t.Fatal(record)
	}
}

func TestConcurrentRedactor(t *testing.T) {
	defer SetRedactor(nil)
	l := New(io.Discard, Options{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				SetRedactor(secretRedactor{})
				l.Info("test", "secret", "value")
				SetRedactor(nil)
			}
		})
	}
	wg.Wait()
}

func TestLogger(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"", "json", "text"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			l := New(&out, Options{Format: format, Level: slog.LevelWarn, AddSource: true})
			l.Info("hidden")
			l.Warn("visible")
			if strings.Contains(out.String(), "hidden") || !strings.Contains(out.String(), "visible") || !strings.Contains(out.String(), "source") {
				t.Fatal(out.String())
			}
			ctx := WithLogger(context.Background(), l)
			if FromContext(ctx) != l || FromContext(context.Background()) != slog.Default() {
				t.Fatal("context logger mismatch")
			}
		})
	}
}

type redactorFunc func(string, slog.Value) slog.Value

func (f redactorFunc) Redact(key string, value slog.Value) slog.Value { return f(key, value) }

func TestRedactorGroups(t *testing.T) {
	defer SetRedactor(nil)
	var out bytes.Buffer
	seen := map[string]bool{}
	SetRedactor(redactorFunc(func(key string, value slog.Value) slog.Value {
		seen[key] = true
		if key == "private" {
			return slog.StringValue("[group removed]")
		}
		return value
	}))
	New(&out, Options{}).Info("test", slog.Group("outer", slog.Group("private", slog.String("secret", "hidden"))))
	for _, key := range []string{"outer", "private", "secret"} {
		if !seen[key] {
			t.Fatalf("attribute %q did not reach redactor", key)
		}
	}
	if strings.Contains(out.String(), "hidden") || !strings.Contains(out.String(), "[group removed]") {
		t.Fatal(out.String())
	}
}
