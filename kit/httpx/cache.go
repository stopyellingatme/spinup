package httpx

import (
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

// DefaultCacheControl sets Cache-Control: private, no-cache before the handler runs.
// A handler that sets its own Cache-Control replaces the value. The default also
// covers a handler that writes nothing and the answer of a recovered panic.
func DefaultCacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-cache")
		next.ServeHTTP(w, r)
	})
}
