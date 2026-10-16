package apidocs

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/kit/httpx"
)

const wantCSP = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

func TestRoutes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ path, contentType, cache, body string }{
		{"/api/openapi.yaml", "application/yaml", "no-cache", "openapi: 3.1.0"},
		{"/api/docs", "text/html; charset=utf-8", "no-cache", "<title>API reference</title>"},
		{"/api/docs/boot.js", "text/javascript; charset=utf-8", "no-cache", "Scalar.createApiReference('#app',"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			mux := http.NewServeMux()
			Mount(mux, Options{Spec: fstest.MapFS{"bundle.gen.yaml": &fstest.MapFile{Data: []byte("openapi: 3.1.0")}}})
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil))
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != tc.contentType || w.Header().Get("Cache-Control") != tc.cache || w.Header().Get("Content-Security-Policy") != wantCSP || !strings.Contains(w.Body.String(), tc.body) {
				t.Fatalf("response: %d %v %.300s", w.Code, w.Header(), w.Body)
			}
			if tc.path == "/api/docs" {
				for _, want := range []string{`id="app"`, `src="/api/docs/scalar.js"`, `src="/api/docs/boot.js"`} {
					if !strings.Contains(w.Body.String(), want) {
						t.Errorf("missing %s", want)
					}
				}
				if strings.Contains(w.Body.String(), "<script>") {
					t.Error("inline script")
				}
			}
			if tc.path == "/api/docs/boot.js" {
				for _, want := range []string{"url: '/api/openapi.yaml'", "withDefaultFonts: false", "telemetry: false", "hideClientButton: false"} {
					if !strings.Contains(w.Body.String(), want) {
						t.Errorf("missing %s", want)
					}
				}
				if strings.Contains(w.Body.String(), "proxyUrl") {
					t.Error("unexpected proxy")
				}
			}
		})
	}
}

func TestScalarEncoding(t *testing.T) {
	t.Parallel()
	compressed, err := os.ReadFile("assets/scalar.js.gz")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	// The vendored standalone bundle exports window.Scalar.createApiReference.
	if !bytes.Contains(plain, []byte("window.Scalar={createApiReference:")) {
		t.Fatal("Scalar global API changed")
	}
	mux := http.NewServeMux()
	Mount(mux, Options{})
	for _, tc := range []struct {
		accept string
		gzip   bool
	}{
		{"", false}, {"gzip", true}, {"br, gzip;q=0.5", true}, {"gzip;q=0", false}, {"gzip;q=0.0, *;q=1", false}, {"*;q=1", true}, {"gzip;q=invalid", false}, {"xgzip", false},
	} {
		t.Run(tc.accept, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/docs/scalar.js", nil)
			r.Header.Set("Accept-Encoding", tc.accept)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			want := plain
			encoding := ""
			if tc.gzip {
				want = compressed
				encoding = "gzip"
			}
			if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != encoding || w.Header().Get("Vary") != "Accept-Encoding" || w.Header().Get("Cache-Control") != "public, max-age=86400" || w.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || w.Header().Get("Content-Security-Policy") != wantCSP || !bytes.Equal(w.Body.Bytes(), want) {
				t.Fatalf("response: %d %v, bytes=%d", w.Code, w.Header(), w.Body.Len())
			}
		})
	}
}

func TestSpecSources(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "bundle.gen.yaml")
	spec := fstest.MapFS{"bundle.gen.yaml": &fstest.MapFile{Data: []byte("embedded")}}
	mux := http.NewServeMux()
	Mount(mux, Options{Spec: spec, Dir: dir})
	for _, value := range []string{"", "disk first", "disk changed", ""} {
		if value != "" {
			if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/openapi.yaml", nil))
		want := value
		if want == "" {
			want = "embedded"
		}
		if w.Code != http.StatusOK || w.Body.String() != want {
			t.Fatalf("response: %d %s", w.Code, w.Body)
		}
	}
}

func TestMissingSpec(t *testing.T) {
	t.Parallel()
	for _, opts := range []Options{{}, {Spec: fstest.MapFS{}}, {Dir: t.TempDir()}} {
		mux := http.NewServeMux()
		Mount(mux, opts)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/openapi.yaml", nil))
		var problem httpx.Problem
		if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusNotFound || problem.Status != http.StatusNotFound || w.Header().Get("Content-Type") != "application/problem+json" || w.Header().Get("Content-Security-Policy") != wantCSP || w.Header().Get("Cache-Control") != "no-cache" || !strings.Contains(problem.Detail, "spinup g resource") || !strings.Contains(problem.Detail, "spinup g openapi") {
			t.Fatalf("response: %d %v %s", w.Code, w.Header(), w.Body)
		}
	}
}

func TestTitleEscaped(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	Mount(mux, Options{Title: `Example </title><script>alert("x")</script>`})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/docs", nil))
	if strings.Contains(w.Body.String(), "<script>alert") || !strings.Contains(w.Body.String(), "Example &lt;/title&gt;&lt;script&gt;") {
		t.Fatal(w.Body)
	}
}
