package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPublicCache(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		maxAge time.Duration
		want   string
	}{
		{2*time.Second + 999*time.Millisecond, "public, max-age=2"},
		{-time.Second, "public, max-age=0"},
	} {
		w := httptest.NewRecorder()
		PublicCache(w, tc.maxAge)
		if got := w.Header().Get("Cache-Control"); got != tc.want {
			t.Errorf("Cache-Control = %q, want %q", got, tc.want)
		}
	}
}

func TestDefaultCacheControl(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, cache string
	}{
		{"default", "private, no-cache"},
		{"handler value", "public, max-age=60"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := DefaultCacheControl(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.name == "handler value" {
					w.Header().Set("Cache-Control", tc.cache)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
			if got := w.Header().Get("Cache-Control"); got != tc.cache {
				t.Errorf("Cache-Control = %q, want %q", got, tc.cache)
			}
		})
	}
}
