package livetest_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/testkit"
	"github.com/stopyellingatme/spinup/kit/x/live"
	"github.com/stopyellingatme/spinup/kit/x/live/livetest"
)

func TestOpenReadsEvents(t *testing.T) {
	t.Parallel()
	var gotUser auth.User
	var gotHeader string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, _ = auth.UserFrom(r.Context())
		gotHeader = r.Header.Get("Last-Event-ID")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_ = http.NewResponseController(w).Flush()
		_, _ = io.WriteString(w, "event: a\nid: 1-1\ndata: {\"x\":1}\n\n: ping\n\nevent: b\ndata: one\ndata: two\n\n")
		_, _ = io.WriteString(w, "event: partial\ndata: lost")
	})
	s := livetest.Open(t, h, "/api/live/notes/live?since=1-0", livetest.WithUser(auth.User{ID: 5, Role: "staff"}), livetest.WithHeader("Last-Event-ID", "1-1"))
	if s.Status() != http.StatusOK || s.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("answer %d %v", s.Status(), s.Header())
	}
	if gotUser.ID != 5 || gotUser.Role != "staff" || gotHeader != "1-1" {
		t.Fatalf("request user %+v header %q", gotUser, gotHeader)
	}
	for _, want := range []livetest.Event{
		{Name: "a", ID: "1-1", Data: `{"x":1}`},
		{Data: "ping"},
		{Name: "b", Data: "one\ntwo"},
	} {
		ev, err := s.Next(time.Second)
		if err != nil || ev != want {
			t.Fatalf("event %+v %v, want %+v", ev, err, want)
		}
	}
	for range 2 {
		if _, err := s.Next(time.Second); !errors.Is(err, io.EOF) {
			t.Fatalf("after the end: %v", err)
		}
	}
	s.Close()
	s.Close()
}

func TestNextTimeoutAndClose(t *testing.T) {
	t.Parallel()
	ended := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(ended)
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done()
	})
	s := livetest.Open(t, h, "/x")
	if s.Status() != http.StatusOK {
		t.Fatal(s.Status())
	}
	if _, err := s.Next(20 * time.Millisecond); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Next on an idle stream: %v", err)
	}
	s.Close()
	select {
	case <-ended:
	default:
		t.Fatal("Close returned before the handler")
	}
}

func TestProblemAnswer(t *testing.T) {
	t.Parallel()
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"status":429}`+"\n")
		w.Header().Set("X-Late", "ignored")
	})
	s := livetest.Open(t, h, "/x")
	if s.Status() != http.StatusTooManyRequests || s.Header().Get("Retry-After") != "5" || s.Header().Get("X-Late") != "" {
		t.Fatalf("answer %d %v", s.Status(), s.Header())
	}
	if _, err := s.Next(time.Second); !errors.Is(err, io.EOF) {
		t.Fatalf("a body with no event: %v", err)
	}
}

func TestWriteDeadline(t *testing.T) {
	t.Parallel()
	result := make(chan error, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rc := http.NewResponseController(w)
		if err := rc.SetWriteDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
			result <- err
			return
		}
		_, _ = io.WriteString(w, ": one\n\n")
		// Nobody reads the second write, so it passes its deadline.
		_, err := io.WriteString(w, ": two\n\n")
		result <- err
	})
	s := livetest.Open(t, h, "/x")
	select {
	case err := <-result:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("the blocked write returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the write deadline did not end the write")
	}
	if ev, err := s.Next(time.Second); err != nil || ev.Data != "one" {
		t.Fatalf("first event %+v %v", ev, err)
	}
	if _, err := s.Next(time.Second); err == nil || errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, io.EOF) {
		t.Fatalf("after a write timeout: %v", err)
	}
}

func TestPanicBecomesAnError(t *testing.T) {
	t.Parallel()
	s := livetest.Open(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }), "/x")
	if _, err := s.Next(time.Second); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("a panic: %v", err)
	}
}

func liveDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL is empty")
	}
	data, err := os.ReadFile("../../../../internal/templates/packs/live/files/api/db/kit/00011_live.sql")
	if err != nil {
		t.Fatal(err)
	}
	kit := fstest.MapFS{"00011_live.sql": &fstest.MapFile{Data: data, Mode: 0o644}}
	return testkit.DBWithMigrations(t, fstest.MapFS{}, testkit.WithKitMigrations(kit))
}

func TestHub(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	h := livetest.Hub(t, pool)
	if err := h.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := livetest.Open(t, h.Stream(func(*http.Request) (live.Key, error) { return "notes", nil }), "/api/live/notes/live")
	if ev, err := s.Next(5 * time.Second); err != nil || ev.Name != "ready" {
		t.Fatalf("ready %+v %v", ev, err)
	}
	// The default heartbeat is 100ms.
	if ev, err := s.Next(5 * time.Second); err != nil || ev.Data != "ping" {
		t.Fatalf("heartbeat %+v %v", ev, err)
	}
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := live.Publish(ctx, tx, "notes", live.Touch("1")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for {
		ev, err := s.Next(5 * time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if ev.Name == "patch" {
			break
		}
	}
	// Options after the defaults replace them.
	quiet := livetest.Hub(t, pool, live.WithHeartbeat(time.Hour))
	s = livetest.Open(t, quiet.Stream(func(*http.Request) (live.Key, error) { return "notes", nil }), "/api/live/notes/live")
	if _, err := s.Next(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if ev, err := s.Next(300 * time.Millisecond); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a heartbeat with WithHeartbeat(1h): %+v %v", ev, err)
	}
}
