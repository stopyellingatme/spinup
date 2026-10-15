package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestDecodeJSON(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		limit      int64
		status     int
	}{
		{"valid", `{"name":"api"}`, 0, 0}, {"syntax", `{"name":`, 0, 400},
		{"unknown", `{"other":1}`, 0, 400}, {"empty", "", 0, 400},
		{"multiple", `{} {}`, 0, 400}, {"trailing invalid", `{} x`, 0, 400},
		{"type", `{"name":1}`, 0, 400}, {"large", `{"name":"too large"}`, 5, 413},
		{"large trailing", `{}           `, 4, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(tc.body))
			if tc.limit > 0 {
				req.Body = http.MaxBytesReader(httptest.NewRecorder(), req.Body, tc.limit)
			}
			var dst struct {
				Name string `json:"name"`
			}
			err := DecodeJSON(req, &dst)
			if tc.status == 0 {
				if err != nil || dst.Name != "api" {
					t.Fatalf("%+v %v", dst, err)
				}
				return
			}
			var typed *Error
			if !errors.As(err, &typed) || ProblemFrom(err).Status != tc.status {
				t.Fatalf("%v", err)
			}
		})
	}
	if p := ProblemFrom(errors.New("internal secret")); p.Status != 500 || p.Detail != "" {
		t.Fatalf("%+v", p)
	}
	want := Problem{Status: 422, Detail: "invalid input"}
	if got := ProblemFrom(fmt.Errorf("wrapped: %w", &Error{Problem: want})); got.Detail != want.Detail {
		t.Fatalf("%+v", got)
	}
}

func TestGoldenErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		limit      int64
	}{
		{"syntax", `{"name":`, 0}, {"unknown", `{"other":1}`, 0},
		{"large", `{"name":"too large"}`, 5}, {"empty", "", 0},
		{"validation", "", 0}, {"panic", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.name == "panic" {
					panic("private panic")
				}
				if tc.name == "validation" {
					WriteProblem(w, r, Problem{Status: 422, Detail: "Validation failed.", Errors: []FieldError{{Field: "name", Message: "is required"}}})
					return
				}
				if tc.limit > 0 {
					r.Body = http.MaxBytesReader(w, r.Body, tc.limit)
				}
				var dst struct {
					Name string `json:"name"`
				}
				WriteProblem(w, r, ProblemFrom(DecodeJSON(r, &dst)))
			}), RequestID(), Recover(quietLogger()))
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/items", strings.NewReader(tc.body))
			r.Header.Set("X-Request-ID", "golden-request")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			got := fmt.Sprintf("HTTP %d\nContent-Type: %s\nX-Request-ID: %s\n\n%s", w.Code, w.Header().Get("Content-Type"), w.Header().Get("X-Request-ID"), w.Body.String())
			path := filepath.Join("testdata", tc.name+".golden")
			if os.Getenv("SPINUP_GOLDEN_UPDATE") == "1" {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("response differs:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestRequestID(t *testing.T) {
	t.Parallel()
	ids := map[string]bool{}
	for _, incoming := range []string{"safe_ID-1.2", strings.Repeat("x", 128), "", "bad id", "é", strings.Repeat("x", 129)} {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		r.Header.Set("X-Request-ID", incoming)
		w := httptest.NewRecorder()
		RequestID()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if RequestIDFrom(r.Context()) != w.Header().Get("X-Request-ID") {
				t.Fatal("context mismatch")
			}
		})).ServeHTTP(w, r)
		got := w.Header().Get("X-Request-ID")
		if !validRequestID(got) {
			t.Fatalf("invalid %q", got)
		}
		if validRequestID(incoming) {
			if got != incoming {
				t.Fatal("incoming ID changed")
			}
		} else {
			if ids[got] {
				t.Fatal("duplicate generated ID")
			}
			ids[got] = true
		}
	}
	if RequestIDFrom(context.Background()) != "" {
		t.Fatal("unexpected ID")
	}
}

func TestMiddleware(t *testing.T) {
	t.Parallel()
	t.Run("chain", func(t *testing.T) {
		var order []string
		wrap := func(name string) Middleware {
			return func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { order = append(order, name); next.ServeHTTP(w, r) })
			}
		}
		Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { order = append(order, "handler") }), wrap("first"), wrap("second")).ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
		if strings.Join(order, ",") != "first,second,handler" {
			t.Fatal(order)
		}
	})
	t.Run("headers", func(t *testing.T) {
		w := httptest.NewRecorder()
		SecurityHeaders()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Frame-Options", "SAMEORIGIN")
			_, _ = w.Write([]byte("ok"))
		})).ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
		for k, v := range map[string]string{"X-Content-Type-Options": "nosniff", "Referrer-Policy": "strict-origin-when-cross-origin", "X-Frame-Options": "SAMEORIGIN", "Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'"} {
			if got := w.Header().Get(k); got != v {
				t.Fatalf("%s: %s", k, got)
			}
		}
	})
	t.Run("max body", func(t *testing.T) {
		MaxBody(3)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			_, err := io.ReadAll(r.Body)
			var size *http.MaxBytesError
			if !errors.As(err, &size) {
				t.Fatalf("%v", err)
			}
		})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader("large")))
	})
	t.Run("recover", func(t *testing.T) {
		var out bytes.Buffer
		w := httptest.NewRecorder()
		Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret") }), RequestID(), Recover(slog.New(slog.NewJSONHandler(&out, nil)))).ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
		if w.Code != 500 || strings.Count(out.String(), "\n") != 1 || !strings.Contains(out.String(), w.Header().Get("X-Request-ID")) || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("%s %s", w.Body.String(), out.String())
		}
	})
	t.Run("abort", func(t *testing.T) {
		defer func() {
			if r, ok := recover().(error); !ok || !errors.Is(r, http.ErrAbortHandler) {
				t.Fatalf("%v", r)
			}
		}()
		Recover(quietLogger())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	})
}

func TestAccessLog(t *testing.T) {
	t.Parallel()
	for _, status := range []int{200, 201, 204} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			w := httptest.NewRecorder()
			h := Chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if status != 200 {
					w.WriteHeader(status)
				}
				if status != 204 {
					_, _ = w.Write([]byte("hello"))
				}
			}), RequestID(), AccessLog(slog.New(slog.NewJSONHandler(&out, nil))))
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/items?secret=hidden", nil)
			r.Header.Set("X-Request-ID", "access-id")
			h.ServeHTTP(w, r)
			var record map[string]any
			if err := json.Unmarshal(out.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			wantBytes := float64(5)
			if status == 204 {
				wantBytes = 0
			}
			if record["status"] != float64(status) || record["bytes"] != wantBytes || record["method"] != "POST" || record["path"] != "/items" || record["request_id"] != "access-id" || record["duration_ms"] == nil {
				t.Fatal(record)
			}
		})
	}
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	if err := WriteJSON(w, 201, map[string]string{"status": "ok"}); err != nil {
		t.Fatal(err)
	}
	if w.Code != 201 || w.Header().Get("Content-Type") != "application/json" || w.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("%+v", w)
	}
	w = httptest.NewRecorder()
	if err := WriteJSON(w, 200, make(chan int)); err == nil || w.Body.Len() != 0 {
		t.Fatal("encoding failure wrote a response")
	}
}

func TestAccessLogPanic(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))
	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("private") }), RequestID(), Recover(logger), AccessLog(logger))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatal(out.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &record); err != nil {
		t.Fatal(err)
	}
	if record["status"] != float64(500) || record["bytes"] != float64(w.Body.Len()) || record["level"] != "INFO" || w.Code != 500 {
		t.Fatalf("%v %d", record, w.Code)
	}
}

func TestResponseController(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush: %v", err)
		}
	}), Recover(quietLogger()), AccessLog(quietLogger()))
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if !w.Flushed {
		t.Fatal("flush was not forwarded")
	}
}
