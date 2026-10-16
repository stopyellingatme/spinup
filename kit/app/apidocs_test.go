package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/kit/config"
)

func TestAPIDocsMount(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, env             string
		flag, option, mounted bool
	}{
		{"production default", "production", false, true, false},
		{"production enabled", "production", true, true, true},
		{"development", "dev", false, true, true},
		{"no option production", "production", true, false, false},
		{"no option development", "dev", false, false, false},
		{"empty environment", "", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := []Option{WithLogger(discardLogger()), WithConfig(config.Base{Env: tc.env, APIDocs: tc.flag})}
			if tc.option {
				opts = append(opts, WithAPIDocs(fstest.MapFS{"bundle.gen.yaml": &fstest.MapFile{Data: []byte("embedded")}}))
			}
			a := New(opts...)
			const csp = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
			for _, path := range []string{"/api/docs", "/api/openapi.yaml", "/api/docs/boot.js", "/api/docs/scalar.js"} {
				w := httptest.NewRecorder()
				r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
				r.Header.Set("Accept-Encoding", "gzip")
				a.Handler().ServeHTTP(w, r)
				want := http.StatusNotFound
				if tc.mounted {
					want = http.StatusOK
				}
				if w.Code != want {
					t.Fatalf("%s: status=%d, want=%d", path, w.Code, want)
				}
				if tc.mounted && w.Header().Get("Content-Security-Policy") != csp {
					t.Fatalf("%s: CSP=%s", path, w.Header().Get("Content-Security-Policy"))
				}
				if w.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatal("default security headers missing")
				}
			}
			if tc.env == "dev" {
				w := httptest.NewRecorder()
				a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/_dev", nil))
				if strings.Contains(w.Body.String(), `href="/api/docs"`) != tc.mounted {
					t.Fatalf("dev link: %s", w.Body)
				}
				if strings.Contains(w.Body.String(), "<script") {
					t.Fatal("dev page contains a script")
				}
			}
		})
	}
}

func TestAPIDocsDevelopmentFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Mkdir("openapi", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("openapi", "bundle.gen.yaml"), []byte("development"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		env, want string
		flag      bool
	}{{"dev", "development", false}, {"production", "embedded", true}} {
		a := New(WithLogger(discardLogger()), WithConfig(config.Base{Env: tc.env, APIDocs: tc.flag}), WithAPIDocs(fstest.MapFS{"bundle.gen.yaml": &fstest.MapFile{Data: []byte("embedded")}}))
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/openapi.yaml", nil))
		if w.Code != http.StatusOK || w.Body.String() != tc.want {
			t.Fatalf("%s: %d %s", tc.env, w.Code, w.Body)
		}
	}
}

func TestAPIDocsNilSpec(t *testing.T) {
	t.Parallel()
	a := New(WithLogger(discardLogger()), WithConfig(config.Base{APIDocs: true}), WithAPIDocs(nil))
	for _, tc := range []struct {
		path   string
		status int
	}{{"/api/docs", http.StatusOK}, {"/api/openapi.yaml", http.StatusNotFound}} {
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil))
		if w.Code != tc.status {
			t.Fatalf("%s: %d", tc.path, w.Code)
		}
	}
}
