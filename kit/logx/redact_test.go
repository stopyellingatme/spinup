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

func TestNestedValueRedaction(t *testing.T) {
	defer SetRedactor(nil)
	SetRedactor(nil)
	type credentials struct {
		Password string
		Name     string
	}
	fields := map[string]any{"nested": map[string]any{
		"password": "hidden-map",
		"headers":  http.Header{"Authorization": {"hidden-auth"}, "Cookie": {"hidden-cookie"}},
		"query":    url.Values{"token": {"hidden-query"}, "page": {"visible-page"}},
		"list":     []any{map[string]string{"secret": "hidden-slice"}, credentials{"hidden-struct", "visible-name"}},
		"url":      "postgres://user:hidden-url@db/app",
	}}
	var out bytes.Buffer
	New(&out, Options{}).Info("nested", "fields", fields)
	if strings.Contains(out.String(), "hidden-") {
		t.Fatalf("nested secrets escaped redaction: %s", out.String())
	}
	for _, visible := range []string{"visible-page", "visible-name"} {
		if !strings.Contains(out.String(), visible) {
			t.Fatalf("missing %q: %s", visible, out.String())
		}
	}
	if fields["nested"].(map[string]any)["password"] != "hidden-map" {
		t.Fatal("redaction mutated the input")
	}
}

func TestRedactionDepthAndCycles(t *testing.T) {
	cycle := map[string]any{"password": "hidden-cycle"}
	cycle["self"] = cycle
	type node struct {
		Password string
		Next     *node
	}
	root := &node{Password: "hidden-pointer"}
	root.Next = root
	deep := map[string]any{"password": "hidden-deep"}
	for range 20 {
		deep = map[string]any{"next": deep}
	}
	for _, value := range []any{cycle, root, deep} {
		var out bytes.Buffer
		New(&out, Options{}).Info("bounded", "value", value)
		if strings.Contains(out.String(), "hidden-") || strings.Contains(out.String(), "!ERROR") || !strings.Contains(out.String(), "****") {
			t.Fatal(out.String())
		}
	}
}
