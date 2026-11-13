package httpx

import (
	"bufio"
	"net"
	"net/http"
	"strconv"
	"time"
)

// PublicCache sets a public Cache-Control header with maxAge in whole seconds.
func PublicCache(w http.ResponseWriter, maxAge time.Duration) {
	seconds := int64(maxAge / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	w.Header().Set("Cache-Control", "public, max-age="+strconv.FormatInt(seconds, 10))
}

// DefaultCacheControl sets private, no-cache when a handler writes no Cache-Control header.
func DefaultCacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&cacheWriter{ResponseWriter: w}, r)
	})
}

type cacheWriter struct {
	http.ResponseWriter
}

func (w *cacheWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *cacheWriter) WriteHeader(status int) {
	w.setDefault()
	w.ResponseWriter.WriteHeader(status)
}

func (w *cacheWriter) Write(data []byte) (int, error) {
	w.setDefault()
	return w.ResponseWriter.Write(data)
}

func (w *cacheWriter) Flush() { _ = w.FlushError() }

func (w *cacheWriter) FlushError() error {
	w.setDefault()
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *cacheWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *cacheWriter) setDefault() {
	if _, ok := w.Header()["Cache-Control"]; !ok {
		w.Header().Set("Cache-Control", "private, no-cache")
	}
}
