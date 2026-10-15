package logx

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestDefaultRedactor(t *testing.T) {
	defer SetRedactor(nil)
	SetRedactor(nil)
	for _, key := range []string{"password", "client_SECRET", "accessToken", "Authorization", "Set-Cookie", "API_KEY", "database_dsn"} {
		t.Run(key, func(t *testing.T) {
			var out bytes.Buffer
			logger := New(&out, Options{}).With(slog.Group("saved", slog.String(key, "hidden-before"), slog.String("label", "visible"))).WithGroup("request")
			logger.Info("test", slog.Group("nested", slog.String(key, "hidden-after"), slog.String("label", "visible")))
			if strings.Contains(out.String(), "hidden-") || strings.Count(out.String(), "****") != 2 || strings.Count(out.String(), "visible") != 2 {
				t.Fatal(out.String())
			}
		})
	}
}

func TestURLRedaction(t *testing.T) {
	defer SetRedactor(nil)
	SetRedactor(nil)
	for _, value := range []string{
		"postgres://user:private@db/app",
		"postgres://:private@db/app",
		"postgres://user:private/part@db/app",
		"postgres://user:private@part@db/app",
		"failed: https://user:private@example.com/path and postgres://other:p%40ss@db/app",
	} {
		t.Run(value, func(t *testing.T) {
			var out bytes.Buffer
			New(&out, Options{}).InfoContext(context.Background(), "connect", "url", value)
			if strings.Contains(out.String(), "private") || strings.Contains(out.String(), "p%40ss") || !strings.Contains(out.String(), ":****@") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestStructuredValueRedaction(t *testing.T) {
	defer SetRedactor(nil)
	SetRedactor(nil)
	values := url.Values{"token": {"query-secret"}, "page": {"1"}}
	headers := http.Header{"Authorization": {"header-secret"}, "Accept": {"application/json"}}
	fields := map[string]string{"password": "map-secret", "name": "visible"}
	var out bytes.Buffer
	New(&out, Options{}).InfoContext(context.Background(), "request",
		slog.Any("fields", fields),
		slog.Any("headers", headers),
		slog.Any("query", values),
	)

	output := out.String()
	for _, secret := range []string{"map-secret", "header-secret", "query-secret"} {
		if strings.Contains(output, secret) {
			t.Fatalf("secret %q was not redacted: %s", secret, output)
		}
	}
	for _, visible := range []string{"visible", "application/json", "\"1\""} {
		if !strings.Contains(output, visible) {
			t.Fatalf("visible value %q was removed: %s", visible, output)
		}
	}
	if strings.Count(output, "****") != 3 {
		t.Fatal(output)
	}
}
