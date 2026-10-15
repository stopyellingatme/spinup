package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPA(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		"index.html":                   {Data: []byte("<!doctype html><title>SPA</title>")},
		"_app/immutable/app.abc123.js": {Data: []byte("console.log('app')")},
		"plain.txt":                    {Data: []byte("plain")},
		"folder/private.txt":           {Data: []byte("private")},
	}
	for _, tc := range []struct {
		method, path         string
		status               int
		cache, content, body string
	}{
		{"GET", "/", 200, "no-cache", "text/html", "SPA"},
		{"GET", "/index.html", 200, "no-cache", "text/html", "SPA"},
		{"GET", "/deep/link", 200, "no-cache", "text/html", "SPA"},
		{"HEAD", "/deep/link", 200, "no-cache", "text/html", ""},
		{"GET", "/_app/immutable/app.abc123.js", 200, "public, max-age=31536000, immutable", "javascript", "console.log"},
		{"HEAD", "/_app/immutable/app.abc123.js", 200, "public, max-age=31536000, immutable", "javascript", ""},
		{"GET", "/plain.txt", 200, "", "text/plain", "plain"},
		{"GET", "/folder/", 200, "no-cache", "text/html", "SPA"},
		{"GET", "/_app/immutable/missing.js", 404, "", "", ""},
		{"GET", "/api/missing", 404, "", "", ""},
		{"GET", "/api", 404, "", "", ""},
		{"GET", "/healthz/missing", 404, "", "", ""},
		{"GET", "/readyz/missing", 404, "", "", ""},
		{"GET", "/metrics", 404, "", "", ""},
		{"POST", "/deep/link", 405, "", "", ""},
		{"GET", "/healthz", 200, "", "application/json", `"status":"ok"`},
		{"GET", "/readyz", 200, "", "application/json", `"status":"ok"`},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			t.Parallel()
			a := New(WithLogger(discardLogger()), WithSPA(files))
			w := httptest.NewRecorder()
			a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil))
			if w.Code != tc.status || w.Header().Get("Cache-Control") != tc.cache || !strings.Contains(w.Header().Get("Content-Type"), tc.content) || !strings.Contains(w.Body.String(), tc.body) {
				t.Fatalf("response = %d %v %s", w.Code, w.Header(), w.Body)
			}
			if tc.status == http.StatusMethodNotAllowed && w.Header().Get("Allow") != "GET, HEAD" {
				t.Fatal("missing allowed methods")
			}
			if tc.method == http.MethodHead && w.Body.Len() != 0 {
				t.Fatal("HEAD returned a body")
			}
			if tc.status != 200 && strings.Contains(w.Body.String(), "SPA") {
				t.Fatal("reserved route returned the SPA")
			}
		})
	}
}

func TestSPAReservedPaths(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{"index.html": {Data: []byte("SPA")}}
	a := New(WithLogger(discardLogger()), WithSPA(files))
	for _, prefix := range []string{"api", "healthz", "readyz", "metrics", "version", "_deploy", "_dev"} {
		for _, method := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
			t.Run(method+"/"+prefix+"/missing", func(t *testing.T) {
				t.Parallel()
				w := httptest.NewRecorder()
				a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), method, "/"+prefix+"/missing", nil))
				if w.Code != http.StatusNotFound || w.Header().Get("Allow") != "" || strings.Contains(w.Body.String(), "SPA") {
					t.Fatalf("response = %d %v %s", w.Code, w.Header(), w.Body)
				}
			})
		}
	}
}
