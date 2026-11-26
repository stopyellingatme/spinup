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
	benchmarkSPA(b, "/_app/immutable/app.js", false)
}

func BenchmarkSPAIndex(b *testing.B) {
	benchmarkSPA(b, "/deep/link", false)
}

func BenchmarkSPAAssetBrotli(b *testing.B) {
	benchmarkSPA(b, "/_app/immutable/app.js", true)
}

func BenchmarkSPAIndexBrotli(b *testing.B) {
	benchmarkSPA(b, "/deep/link", true)
}

func benchmarkSPA(b *testing.B, path string, brotli bool) {
	b.Helper()
	run := newBenchmarkSPABody(path, brotli)
	b.ReportAllocs()
	for b.Loop() {
		run()
	}
}

func newBenchmarkSPABody(path string, brotli bool) func() {
	files := fstest.MapFS{
		"index.html":            {Data: []byte("<!doctype html><title>SPA</title><script>console.log('SPA')</script>")},
		"_app/immutable/app.js": {Data: []byte(strings.Repeat("console.log('app');\n", 2048))},
	}
	if brotli {
		// These fixtures contain the Brotli encoding of the files above.
		files["index.html.br"] = &fstest.MapFile{Data: []byte("\x1f\x43\x00\xe0\x9d\x07\x76\x03\xfd\x84\x48\x4b\x85\x71\xe1\x85\xd5\x27\x2f\x3c\x53\x12\x2a\x85\x2d\x83\x50\xde\xca\xe5\x0c\x36\xe0\xc0\xa9\x8c\x12\x9a\xfd\x0e\x60\x44\x2c\x4b\xba\x49\xbc\xc0\xb2\xef\xc7\x50\x36")}
		files["_app/immutable/app.js.br"] = &fstest.MapFile{Data: []byte("\x1f\xff\x9f\xf8\x8d\xd3\x25\xb6\xf6\xa8\x4c\x1e\x70\x97\x18\xf9\xca\x08\xa9\xb0\xf3\x56\x85\x1c\x58\x23\xf0\xe2\xdd\xf4\x12\xc0\xbb\x44\x7e\x28\x00")}
	}
	handler := New(WithLogger(discardLogger()), WithSPA(files)).Handler()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	req.Header.Set("X-Request-ID", "spa-benchmark")
	if brotli {
		req.Header.Set("Accept-Encoding", "br")
	}
	return func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			panic(recorder.Code)
		}
		if brotli && recorder.Header().Get("Content-Encoding") != "br" {
			panic("missing Brotli response")
		}
	}
}
