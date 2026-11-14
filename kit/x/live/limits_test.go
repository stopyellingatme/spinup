package live_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/x/live"
	"github.com/stopyellingatme/spinup/kit/x/live/livetest"
)

// openProblem opens a stream that must be refused and checks the answer.
func openProblem(t *testing.T, h http.Handler, status int, retryAfter string, opts ...livetest.RequestOption) {
	t.Helper()
	s := livetest.Open(t, h, "/api/live/notes/live", opts...)
	defer s.Close()
	if s.Status() != status || s.Header().Get("Retry-After") != retryAfter || s.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("answer %d %v, want %d with Retry-After %q", s.Status(), s.Header(), status, retryAfter)
	}
}

func TestStreamUserLimit(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	opt, reader := meter(t)
	h := livetest.Hub(t, pool, opt, live.WithMaxStreamsPerUser(2))
	stream := h.Stream(shared("notes"))
	alice := livetest.WithUser(auth.User{ID: 1})
	first := livetest.Open(t, stream, "/api/live/notes/live", alice)
	livetest.Open(t, stream, "/api/live/notes/live", alice)
	rec := get(stream, "/api/live/notes/live", alice)
	problem(t, rec, http.StatusTooManyRequests, "This account has 2 open live streams. Try: close another tab, then connect again.", "5")
	// Another user has slots of its own.
	next(t, livetest.Open(t, stream, "/api/live/notes/live", livetest.WithUser(auth.User{ID: 2})), "ready")
	// A closed stream frees its slot.
	first.Close()
	next(t, livetest.Open(t, stream, "/api/live/notes/live", alice), "ready")
	openProblem(t, stream, http.StatusTooManyRequests, "5", alice)

	// A stream with no user counts under the ID 0.
	livetest.Open(t, stream, "/api/live/notes/live")
	livetest.Open(t, stream, "/api/live/notes/live")
	openProblem(t, stream, http.StatusTooManyRequests, "5")
	if n := points(t, reader, "live.stream.opened", "live.result", "limit_user"); n != 3 {
		t.Fatalf("limit_user %d", n)
	}
}

func TestStreamGlobalLimit(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	opt, reader := meter(t)
	h := livetest.Hub(t, pool, opt, live.WithMaxStreams(2))
	stream := h.Stream(shared("notes"))
	first := livetest.Open(t, stream, "/api/live/notes/live", livetest.WithUser(auth.User{ID: 1}))
	livetest.Open(t, stream, "/api/live/notes/live", livetest.WithUser(auth.User{ID: 2}))
	rec := get(stream, "/api/live/notes/live", livetest.WithUser(auth.User{ID: 3}))
	problem(t, rec, http.StatusServiceUnavailable, "The server has the most live streams that it allows (2). Try: connect again in a few seconds.", "5")
	first.Close()
	next(t, livetest.Open(t, stream, "/api/live/notes/live", livetest.WithUser(auth.User{ID: 3})), "ready")
	if n := points(t, reader, "live.stream.opened", "live.result", "limit_global"); n != 1 {
		t.Fatalf("limit_global %d", n)
	}
	if n := points(t, reader, "live.stream.active", "", ""); n != 2 {
		t.Fatalf("live.stream.active %d", n)
	}
}

func TestStreamBootstrapBusy(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	opt, reader := meter(t)
	h := livetest.Hub(t, pool, opt, live.WithBootstrapLimit(1), live.WithBootstrapWait(100*time.Millisecond))
	// A snapshot whose load waits holds the only bootstrap slot.
	entered, leave := make(chan struct{}), make(chan struct{})
	held := live.Snapshot(h, shared("notes"), func(context.Context, live.Key, int) ([]int, error) {
		close(entered)
		<-leave
		return nil, nil
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		get(held, "/api/live/notes")
	}()
	<-entered
	stream := h.Stream(shared("other"))
	start := time.Now()
	problem(t, get(stream, "/api/live/other/live"), http.StatusServiceUnavailable, "The server is loading other feeds. Try: connect again in a few seconds.", "2")
	if d := time.Since(start); d < 100*time.Millisecond {
		t.Fatalf("the stream waited %s for a slot, want 100ms", d)
	}
	problem(t, get(live.Snapshot(h, shared("other"), func(context.Context, live.Key, int) ([]int, error) { return nil, nil }), "/api/live/other"),
		http.StatusServiceUnavailable, "The server is loading other feeds.", "2")
	close(leave)
	<-done
	// The slot is free again: the feed loads.
	next(t, livetest.Open(t, stream, "/api/live/other/live"), "ready")
	if n := points(t, reader, "live.stream.opened", "live.result", "busy"); n != 1 {
		t.Fatalf("busy %d", n)
	}
	if n := points(t, reader, "live.bootstrap.rejected", "", ""); n != 2 {
		t.Fatalf("live.bootstrap.rejected %d", n)
	}
}
