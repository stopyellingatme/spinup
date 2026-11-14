package devtools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type testConfig struct {
	DatabaseURL string `env:"DATABASE_URL"`
	Password    string
	Public      string
}

func TestHandlerMasksAndEscapes(t *testing.T) {
	t.Parallel()
	d := New(Options{Name: "todo", Version: "1.0", Config: testConfig{
		DatabaseURL: "postgres://app:password@db/todo",
		Password:    "hunter2",
		Public:      "<script>alert(1)</script>",
	}})
	w := httptest.NewRecorder()
	d.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/_dev", nil))
	body := w.Body.String()
	for _, want := range []string{"postgres://app:****@db/todo", "****", "&lt;script&gt;alert(1)&lt;/script&gt;"} {
		if !strings.Contains(body, want) {
			t.Fatalf("page does not contain %q: %s", want, body)
		}
	}
	for _, secret := range []string{"password@", "hunter2", "<script>"} {
		if strings.Contains(body, secret) {
			t.Fatalf("page exposes %q: %s", secret, body)
		}
	}
	if w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatal(w.Header())
	}
}

func TestMiddlewareRecordsConcurrentRequests(t *testing.T) {
	t.Parallel()
	d := New(Options{})
	h := d.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) }))
	var group sync.WaitGroup
	for range 80 {
		group.Add(1)
		go func() {
			defer group.Done()
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/items", nil)
			r.Header.Set("X-Request-ID", "request-1")
			h.ServeHTTP(httptest.NewRecorder(), r)
		}()
	}
	group.Wait()
	w := httptest.NewRecorder()
	d.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/_dev/requests.json", nil))
	if w.Code != http.StatusOK || strings.Count(w.Body.String(), `"path":"/items"`) != 50 || !strings.Contains(w.Body.String(), `"status":201`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

// deadlineRecorder is a recorder that also controls its write deadline.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline bool
}

func (w *deadlineRecorder) SetWriteDeadline(time.Time) error { w.deadline = true; return nil }

func TestDevtoolsStatusWriterFlushes(t *testing.T) {
	t.Parallel()
	d := New(Options{})
	under := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	h := d.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		controller := http.NewResponseController(w)
		if err := controller.SetWriteDeadline(time.Time{}); err != nil {
			t.Errorf("SetWriteDeadline through the devtools writer: %v", err)
		}
		_, _ = w.Write([]byte("data: 1\n\n"))
		if err := controller.Flush(); err != nil {
			t.Errorf("Flush through the devtools writer: %v", err)
		}
	}))
	h.ServeHTTP(under, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/events", nil))
	if !under.Flushed || !under.deadline || under.Body.String() != "data: 1\n\n" {
		t.Fatalf("flushed=%v deadline=%v body=%q", under.Flushed, under.deadline, under.Body.String())
	}
}
