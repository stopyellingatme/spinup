package httpx

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Middleware wraps an HTTP handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware with the first middleware outermost.
func Chain(h http.Handler, m ...Middleware) http.Handler {
	for i := len(m) - 1; i >= 0; i-- {
		h = m[i](h)
	}
	return h
}

type requestIDKey struct{}

// RequestID accepts a safe X-Request-ID or generates a cryptographically random ID.
// IDs contain 1 to 128 ASCII letters, digits, periods, underscores, or hyphens.
// The ID is available in the response header and request context.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-ID")
			if !validRequestID(id) {
				var data [16]byte
				_, _ = rand.Read(data[:])
				id = hex.EncodeToString(data[:])
			}
			w.Header().Set("X-Request-ID", id)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
		})
	}
}

// RequestIDFrom returns the request ID, or an empty string when none is present.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
func validRequestID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

type recoveryKey struct{}

type recoveryState struct {
	response *responseWriter
	after    []func()
}

// Recover converts handler panics to a 500 problem and logs one error with the ID.
// It re-panics http.ErrAbortHandler. A panic after headers aborts the response.
// A nil logger uses slog.Default.
func Recover(l *slog.Logger) Middleware {
	if l == nil {
		l = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			response := &responseWriter{ResponseWriter: w}
			state := &recoveryState{response: response}
			defer func() {
				defer func() {
					for _, after := range state.after {
						after()
					}
				}()
				if value := recover(); value != nil {
					if err, ok := value.(error); ok && errors.Is(err, http.ErrAbortHandler) {
						panic(http.ErrAbortHandler)
					}
					l.ErrorContext(r.Context(), "HTTP handler panicked", "request_id", RequestIDFrom(r.Context()), "panic", value)
					if response.status != 0 {
						panic(http.ErrAbortHandler)
					}
					WriteProblem(response, r, Problem{Status: http.StatusInternalServerError})
				}
			}()
			next.ServeHTTP(response, r.WithContext(context.WithValue(r.Context(), recoveryKey{}, state)))
		})
	}
}

// AccessLog writes one info record per request with method, path, status, bytes,
// duration_ms, and request_id. A nil logger uses slog.Default.
func AccessLog(l *slog.Logger) Middleware {
	if l == nil {
		l = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			if state, ok := r.Context().Value(recoveryKey{}).(*recoveryState); ok {
				// Recovery writes its problem after the inner handler unwinds.
				// Delay this record so it includes the final status and bytes.
				state.after = append(state.after, func() { accessRecord(l, r, state.response, start, nil) })
				next.ServeHTTP(w, r)
				return
			}
			response := &responseWriter{ResponseWriter: w}
			defer func() {
				value := recover()
				accessRecord(l, r, response, start, value)
				if value != nil {
					panic(value)
				}
			}()
			next.ServeHTTP(response, r)
		})
	}
}

func accessRecord(l *slog.Logger, r *http.Request, response *responseWriter, start time.Time, panicValue any) {
	status := response.status
	if status == 0 {
		status = http.StatusOK
		if panicValue != nil {
			status = http.StatusInternalServerError
		}
	}
	l.InfoContext(r.Context(), "HTTP request", "method", r.Method, "path", r.URL.Path, "status", status, "bytes", response.bytes, "duration_ms", float64(time.Since(start))/float64(time.Millisecond), "request_id", RequestIDFrom(r.Context()))
}

// SecurityHeaders sets conservative defaults. A handler can replace each header.
// Headers already present on the response are preserved.
func SecurityHeaders() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, header := range [][2]string{
				{"X-Content-Type-Options", "nosniff"},
				{"Referrer-Policy", "strict-origin-when-cross-origin"},
				{"X-Frame-Options", "DENY"},
				{"Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'"},
			} {
				if _, ok := w.Header()[header[0]]; !ok {
					w.Header().Set(header[0], header[1])
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// MaxBody limits the request body to n bytes using http.MaxBytesReader.
func MaxBody(n int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, n)
			}
			next.ServeHTTP(w, r)
		})
	}
}

type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += int64(n)
	return n, err
}
func (w *responseWriter) Flush() { _ = w.FlushError() }
func (w *responseWriter) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}
