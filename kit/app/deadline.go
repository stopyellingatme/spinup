package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/kit/httpx"
)

func (a *App) requestDeadline() httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			devtools := a.config.Env == "dev" && (r.URL.Path == "/_dev" || strings.HasPrefix(r.URL.Path, "/_dev/"))
			if devtools || a.timeoutExempt != nil && a.timeoutExempt(r) {
				next.ServeHTTP(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), a.requestTimeout)
			defer cancel()
			r = r.WithContext(ctx)
			response := &deadlineWriter{ResponseWriter: w, request: r}
			next.ServeHTTP(response, r)
			response.expired()
		})
	}
}

// Keep writes synchronous so handlers retain normal streaming and panic behavior.
// Cancellation stops database waits. A response already sent cannot become a problem.
type deadlineWriter struct {
	http.ResponseWriter
	request *http.Request
	wrote   bool
}

func (w *deadlineWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *deadlineWriter) expired() bool {
	if !errors.Is(w.request.Context().Err(), context.DeadlineExceeded) {
		return false
	}
	if !w.wrote {
		w.wrote = true
		w.Header().Del("Content-Length")
		w.Header().Del("Content-Encoding")
		// Allow the problem to reach the client when WriteTimeout also elapsed.
		_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(time.Second))
		httpx.WriteProblem(w.ResponseWriter, w.request, httpx.Problem{
			Status: http.StatusServiceUnavailable,
			Detail: "The request deadline expired. Try: retry the request later.",
		})
	}
	return true
}

func (w *deadlineWriter) WriteHeader(status int) {
	if w.expired() || w.wrote {
		return
	}
	if status >= 200 || status == http.StatusSwitchingProtocols {
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *deadlineWriter) Write(data []byte) (int, error) {
	if w.expired() {
		return 0, http.ErrHandlerTimeout
	}
	w.wrote = true
	return w.ResponseWriter.Write(data)
}

func (w *deadlineWriter) Flush() { _ = w.FlushError() }

func (w *deadlineWriter) FlushError() error {
	if w.expired() {
		return http.ErrHandlerTimeout
	}
	w.wrote = true
	return http.NewResponseController(w.ResponseWriter).Flush()
}
