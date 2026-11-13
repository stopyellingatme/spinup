package logx_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/stopyellingatme/spinup/kit/httpx"
	"github.com/stopyellingatme/spinup/kit/logx"
)

func BenchmarkAccessLine(b *testing.B) {
	for _, tc := range []struct{ name, path string }{
		{"Plain", "/api/notes"},
		{"URL", "/proxy/https://user:password@example.com/notes"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			run := newBenchmarkAccessLineBody(tc.path)
			b.ReportAllocs()
			for b.Loop() {
				run()
			}
		})
	}
}

func newBenchmarkAccessLineBody(path string) func() {
	logger := logx.New(io.Discard, logx.Options{})
	handler := httpx.AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	request := (&http.Request{Method: http.MethodGet, URL: &url.URL{Path: path}}).WithContext(context.Background())
	writer := &benchmarkWriter{header: make(http.Header)}
	return func() { handler.ServeHTTP(writer, request) }
}

type benchmarkWriter struct{ header http.Header }

func (w *benchmarkWriter) Header() http.Header          { return w.header }
func (*benchmarkWriter) WriteHeader(int)                {}
func (*benchmarkWriter) Write(data []byte) (int, error) { return len(data), nil }
