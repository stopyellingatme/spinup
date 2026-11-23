package console

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestUnknownRoutesFailClosed(t *testing.T) {
	t.Parallel()
	now := func() time.Time { return time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC) }
	cfg := config{App: "todo", Env: "production", Profile: "absent", Backend: "none"}
	for name, handler := range map[string]http.Handler{
		"query": queryHandler(http.DefaultClient, now),
		"page":  webHandler(cfg, fstest.MapFS{}, http.DefaultClient, now),
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1/unknown", nil))
			if response.Code != http.StatusNotFound && response.Code != http.StatusServiceUnavailable {
				t.Fatalf("unknown route returned %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestMissingInputCannotProduceHealthyRelease(t *testing.T) {
	t.Parallel()
	cfg := config{App: "todo", Env: "production", Profile: "absent", Backend: "none"}
	result, err := project(fstest.MapFS{}, cfg, time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC))
	if err != nil {
		if !strings.Contains(err.Error(), "\nTry:") {
			t.Fatalf("project error lacks repair instructions: %v", err)
		}
		return
	}
	if result.Release.State != "unavailable" || result.Release.Data != nil {
		t.Fatalf("missing input produced a release: %+v", result.Release)
	}
}

func TestMainInvalidArguments(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"unknown"}, {"serve", "extra"}, {"healthcheck", "serve"}, {"query", "extra"}} {
		var stdout, stderr bytes.Buffer
		if code := Main(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "\nTry:") {
			t.Fatalf("Main(%q) = %d, stdout=%q, stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}
