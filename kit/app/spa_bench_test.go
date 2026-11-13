package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func BenchmarkSPAAsset(b *testing.B) {
	benchmarkSPA(b, "/_app/immutable/app.js")
}

func BenchmarkSPAIndex(b *testing.B) {
	benchmarkSPA(b, "/deep/link")
}

func benchmarkSPA(b *testing.B, path string) {
	b.Helper()
	files := fstest.MapFS{
		"index.html":            {Data: []byte("<!doctype html><title>SPA</title><script>console.log('SPA')</script>")},
		"_app/immutable/app.js": {Data: []byte(strings.Repeat("console.log('app');\n", 2048))},
	}
	handler := New(WithLogger(discardLogger()), WithSPA(files)).Handler()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	req.Header.Set("X-Request-ID", "spa-benchmark")
	b.ReportAllocs()
	for b.Loop() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			b.Fatal(recorder.Code)
		}
	}
}
