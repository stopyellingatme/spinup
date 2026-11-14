package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func BenchmarkMiddlewareChain(b *testing.B) {
	handler := New(WithLogger(discardLogger()), WithRoutes(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /bench", func(http.ResponseWriter, *http.Request) {})
	})).Handler()
	for _, tc := range []struct{ name, requestID string }{{"generated", ""}, {"provided", "benchmark-request-id"}} {
		b.Run(tc.name, func(b *testing.B) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/bench", nil)
			if tc.requestID != "" {
				req.Header.Set("X-Request-ID", tc.requestID)
			}
			run := benchmarkMiddlewareChainBody(handler, req)
			b.ReportAllocs()
			for b.Loop() {
				run()
			}
		})
	}
}

func benchmarkMiddlewareChainBody(handler http.Handler, req *http.Request) func() {
	return func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			panic(recorder.Code)
		}
	}
}
