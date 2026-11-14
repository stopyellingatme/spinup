package live_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/x/live"
	"github.com/stopyellingatme/spinup/kit/x/live/livetest"
)

func TestDrainEndsStreams(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	opt, reader := meter(t)
	h := livetest.Hub(t, pool, opt, live.WithHeartbeat(time.Hour))
	stream := h.Stream(shared("notes"))
	var open []*livetest.Stream
	for i := range 3 {
		s := livetest.Open(t, stream, "/api/live/notes/live", livetest.WithUser(auth.User{ID: int64(i + 1)}))
		next(t, s, "ready")
		open = append(open, s)
	}
	if err := h.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := h.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("Drain took %s with 3 open streams", d)
	}
	for _, s := range open {
		if ev, _ := next(t, s, "bye"); ev.Data != `{"reason":"drain"}` {
			t.Fatalf("bye %+v", ev)
		}
		end(t, s)
	}
	problem(t, get(stream, "/api/live/notes/live"), http.StatusServiceUnavailable, "The server is shutting down. Try: connect again.", "1")
	if err := h.Ready(t.Context()); err == nil || !strings.Contains(err.Error(), "draining") {
		t.Fatalf("Ready after Drain: %v", err)
	}
	if err := h.Drain(t.Context()); err != nil {
		t.Fatalf("second Drain: %v", err)
	}
	if n := points(t, reader, "live.stream.duration", "live.end", "drain"); n != 3 {
		t.Fatalf("end=drain %d", n)
	}
	if n := points(t, reader, "live.stream.opened", "live.result", "drain"); n != 1 {
		t.Fatalf("result=drain %d", n)
	}
	// A snapshot still works during the drain.
	rec := get(live.Snapshot(h, shared("notes"), func(context.Context, live.Key, int) ([]int, error) { return nil, nil }), "/api/live/notes")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot during drain %d", rec.Code)
	}
}

func TestDrainWithNoStream(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	h := livetest.Hub(t, pool)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := h.Drain(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDrainContextEnds(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	h := livetest.Hub(t, pool, live.WithHeartbeat(time.Hour))
	s := livetest.Open(t, h.Stream(shared("notes")), "/api/live/notes/live")
	// The ready event waits in the reader, so the bye write of the handler waits too.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := h.Drain(ctx)
	var le *live.Error
	if !errors.Is(err, context.Canceled) || !errors.As(err, &le) || !strings.Contains(err.Error(), "\nTry: ") {
		t.Fatalf("Drain with an expired context: %v", err)
	}
	next(t, s, "ready")
	next(t, s, "bye")
	end(t, s)
	if err := h.Drain(t.Context()); err != nil {
		t.Fatalf("Drain after the streams ended: %v", err)
	}
}

func TestDrainBeforeStart(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	h, err := live.New(pool, live.WithLogger(discard()))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := h.Ready(t.Context()); err == nil {
		t.Fatal("Ready after Drain returned nil")
	}
	if err := h.Start(t.Context()); err == nil {
		t.Fatal("Start after Drain returned nil")
	}
	if err := h.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := h.Drain(t.Context()); err != nil {
		t.Fatalf("Drain after Close: %v", err)
	}
	problem(t, get(h.Stream(shared("notes")), "/api/live/notes/live"), http.StatusServiceUnavailable, "The server is shutting down.", "1")
}
