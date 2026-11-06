package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/config"
	"github.com/stopyellingatme/spinup/kit/httpx"
)

func TestRequestDeadline(t *testing.T) {
	t.Setenv("REQUEST_TIMEOUT", "20ms")
	for _, writeError := range []bool{false, true} {
		a := New(WithLogger(discardLogger()), WithRoutes(func(mux *http.ServeMux) {
			mux.HandleFunc("GET /wait", func(w http.ResponseWriter, r *http.Request) {
				if _, ok := r.Context().Deadline(); !ok {
					t.Error("request has no deadline")
					return
				}
				<-r.Context().Done()
				if writeError {
					httpx.WriteProblem(w, r, httpx.Problem{Status: 500, Detail: "private database error"})
				}
			})
		}))
		w := httptest.NewRecorder()
		start := time.Now()
		a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), "GET", "/wait", nil))
		if time.Since(start) > time.Second {
			t.Fatal("handler did not stop at the request deadline")
		}
		var problem httpx.Problem
		if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
			t.Fatal(err)
		}
		if w.Code != 503 || problem.Status != 503 || w.Header().Get("Content-Type") != "application/problem+json" {
			t.Fatalf("response = %d %s", w.Code, w.Body)
		}
	}
}

func TestDevtoolsRequestDeadlineExemption(t *testing.T) {
	t.Setenv("REQUEST_TIMEOUT", "20ms")
	for _, path := range []string{"/_dev", "/_dev/requests.json"} {
		a := New(WithLogger(discardLogger()), WithConfig(config.Base{Env: "dev"}), WithMiddleware(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, ok := r.Context().Deadline(); ok {
					t.Error("devtools request has an application deadline")
				}
				next.ServeHTTP(w, r)
			})
		}))
		a.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), "GET", path, nil))
	}
}

func TestRequestDeadlineOptions(t *testing.T) {
	t.Setenv("REQUEST_TIMEOUT", "2s")
	a := New(WithLogger(discardLogger()), WithRequestTimeout(time.Second),
		WithRequestTimeoutExempt(func(r *http.Request) bool { return r.URL.Path == "/events" }),
		WithRoutes(func(mux *http.ServeMux) {
			mux.HandleFunc("/", func(_ http.ResponseWriter, r *http.Request) {
				deadline, ok := r.Context().Deadline()
				if r.URL.Path == "/events" {
					if ok {
						t.Error("streaming route has an application deadline")
					}
					return
				}
				if !ok || time.Until(deadline) > time.Second {
					t.Error("request timeout option did not override the environment")
				}
			})
		}))
	for _, path := range []string{"/events", "/ordinary"} {
		a.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), "GET", path, nil))
	}
	for _, raw := range []string{"invalid", "0", "-1s", ""} {
		t.Setenv("REQUEST_TIMEOUT", raw)
		if got := New(WithLogger(discardLogger())).requestTimeout; got != 30*time.Second {
			t.Errorf("REQUEST_TIMEOUT=%q: timeout = %s", raw, got)
		}
	}
}

func TestRequestDeadlinePreservesParent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	want, _ := ctx.Deadline()
	a := New(WithLogger(discardLogger()), WithRequestTimeout(time.Minute), WithRoutes(func(mux *http.ServeMux) {
		mux.HandleFunc("/", func(_ http.ResponseWriter, r *http.Request) {
			if got, _ := r.Context().Deadline(); !got.Equal(want) {
				t.Errorf("deadline = %s, want parent deadline %s", got, want)
			}
		})
	}))
	a.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, "GET", "/", nil))
}

func TestRequestDeadlineAfterResponseStarts(t *testing.T) {
	a := New(WithLogger(discardLogger()), WithRequestTimeout(20*time.Millisecond), WithRoutes(func(mux *http.ServeMux) {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("accepted"))
			<-r.Context().Done()
			if _, err := w.Write([]byte("late")); !errors.Is(err, http.ErrHandlerTimeout) {
				t.Errorf("late write error = %v", err)
			}
		})
	}))
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), "GET", "/", nil))
	if w.Code != http.StatusAccepted || w.Body.String() != "accepted" {
		t.Fatalf("started response changed: %d %s", w.Code, w.Body)
	}
}
