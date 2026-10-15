package deployx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInfoAccess(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("shared-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h := New(Options{Version: "v1", TokenFile: file, MigrationVersion: func(context.Context) (string, error) { return "42", nil }})
	for _, tt := range []struct {
		addr, token string
		status      int
	}{
		{"8.8.8.8:20", "", 403}, {"10.0.0.2:20", "", 200}, {"[::1]:20", "", 200}, {"8.8.8.8:20", "shared-secret", 200}, {"8.8.8.8:20", "wrong", 403}, {"bad", "", 403},
	} {
		t.Run(tt.addr+tt.token, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_deploy/info", nil)
			r.RemoteAddr = tt.addr
			r.Header.Set("Authorization", "Bearer "+tt.token)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("status %d", w.Code)
			}
			if w.Code == 200 && !strings.Contains(w.Body.String(), `"migration_version":"42"`) {
				t.Fatal(w.Body.String())
			}
		})
	}
}

func TestInfoFailsClosed(t *testing.T) {
	t.Parallel()
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"/missing/deployx-token", empty} {
		h := New(Options{TokenFile: file})
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_deploy/info", nil)
		r.RemoteAddr = "8.8.8.8:1"
		r.Header.Set("Authorization", "Bearer ")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatal(w.Code)
		}
	}
}

func TestInfoMigrationFailure(t *testing.T) {
	t.Parallel()
	h := New(Options{MigrationVersion: func(context.Context) (string, error) { return "", errors.New("sensitive database error") }})
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_deploy/info", nil)
	r.RemoteAddr = "127.0.0.1:20"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "sensitive") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestInfoForwardedRequestsRequireToken(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("token"), 0600); err != nil {
		t.Fatal(err)
	}
	h := New(Options{TokenFile: file})
	for _, header := range []string{"Forwarded", "X-Forwarded-For", "X-Real-IP"} {
		for _, token := range []string{"", "token"} {
			t.Run(header+token, func(t *testing.T) {
				t.Parallel()
				r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_deploy/info", nil)
				r.RemoteAddr = "10.0.0.2:123"
				r.Header.Set(header, "127.0.0.1")
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				want := http.StatusForbidden
				if token != "" {
					want = http.StatusOK
				}
				if w.Code != want {
					t.Fatal(w.Code, want)
				}
			})
		}
	}
}
