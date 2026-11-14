package live_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/x/live"
	"github.com/stopyellingatme/spinup/kit/x/live/livetest"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type note struct {
	ID   int    `json:"id"`
	Body string `json:"body"`
}

func TestSnapshotReadsTheCursorFirst(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	put(t, pool, "notes", 1, 2)
	epoch := epochOf(t, pool)
	opt, reader := meter(t)
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	h := livetest.Hub(t, pool, opt, live.WithTracerProvider(tp))
	// The loader runs after the cursor read. A write that commits during it is
	// in the rows, and its patch replays after the cursor.
	snapshot := live.Snapshot(h, shared("notes"), func(ctx context.Context, key live.Key, limit int) ([]note, error) {
		if key != "notes" || limit != 501 {
			return nil, fmt.Errorf("key %q limit %d", key, limit)
		}
		put(t, pool, "notes", 3)
		return []note{{1, "a"}, {2, "b"}, {3, "c"}}, nil
	})
	rec := get(snapshot, "/api/live/notes")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("answer %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	want := fmt.Sprintf(`{"epoch":%d,"seq":2,"rows":[{"id":1,"body":"a"},{"id":2,"body":"b"},{"id":3,"body":"c"}]}`+"\n", epoch)
	if rec.Body.String() != want {
		t.Fatalf("body %s, want %s", rec.Body, want)
	}
	s := livetest.Open(t, h.Stream(shared("notes")), fmt.Sprintf("/api/live/notes/live?since=%d-2", epoch))
	next(t, s, "ready")
	if got := patchSeqs(t, s, 1); !equal(got, []int64{3}) {
		t.Fatalf("replay %v", got)
	}
	if points(t, reader, "live.snapshot.duration", "", "") != 1 {
		t.Error("no live.snapshot.duration point")
	}
	found := false
	for _, span := range spans.Ended() {
		if span.Name() == "live.snapshot" {
			found = true
		}
	}
	if !found {
		t.Error("no live.snapshot span")
	}
}

func TestSnapshotRowsAndMore(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	h := livetest.Hub(t, pool, live.WithSnapshotLimit(3))
	rows := shared("notes")
	empty := live.Snapshot(h, rows, func(context.Context, live.Key, int) ([]note, error) { return nil, nil })
	if rec := get(empty, "/api/live/notes"); rec.Code != http.StatusOK || rec.Body.String() != `{"epoch":0,"seq":0,"rows":[]}`+"\n" {
		t.Fatalf("empty %d %s", rec.Code, rec.Body)
	}
	var gotLimit int
	full := live.Snapshot(h, rows, func(_ context.Context, _ live.Key, limit int) ([]note, error) {
		gotLimit = limit
		return []note{{1, "a"}, {2, "b"}, {3, "c"}, {4, "d"}}, nil
	})
	rec := get(full, "/api/live/notes")
	var body struct {
		Rows []note `json:"rows"`
		More *bool  `json:"more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || gotLimit != 4 || len(body.Rows) != 3 || body.More == nil || !*body.More {
		t.Fatalf("cut snapshot (limit %d): %s %v", gotLimit, rec.Body, err)
	}
	exact := live.Snapshot(h, rows, func(context.Context, live.Key, int) ([]note, error) {
		return []note{{1, "a"}, {2, "b"}, {3, "c"}}, nil
	})
	if rec := get(exact, "/api/live/notes"); strings.Contains(rec.Body.String(), "more") {
		t.Fatalf("a snapshot of exactly the limit has more: %s", rec.Body)
	}
	// A loader that reads one page of a longer list marks the cut itself.
	page := live.Snapshot(h, rows, func(ctx context.Context, _ live.Key, _ int) ([]note, error) {
		live.MarkMore(ctx)
		return []note{{1, "a"}, {2, "b"}}, nil
	})
	if rec := get(page, "/api/live/notes"); !strings.HasSuffix(rec.Body.String(), `"rows":[{"id":1,"body":"a"},{"id":2,"body":"b"}],"more":true}`+"\n") {
		t.Fatalf("a marked snapshot: %s", rec.Body)
	}
	// MarkMore outside a snapshot load does nothing, and the mark does not
	// stay for the next request.
	live.MarkMore(t.Context())
	if rec := get(exact, "/api/live/notes"); strings.Contains(rec.Body.String(), "more") {
		t.Fatalf("a mark stays after its request: %s", rec.Body)
	}
}

func TestSnapshotFailures(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	logs := &lockedBuffer{}
	opt, reader := meter(t)
	h := livetest.Hub(t, pool, opt, live.WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	failing := live.Snapshot(h, shared("notes"), func(context.Context, live.Key, int) ([]note, error) {
		return nil, errors.New("statement timeout")
	})
	problem(t, get(failing, "/api/live/notes"), http.StatusServiceUnavailable, "Try: retry the request later.", "")
	if text := logs.String(); !strings.Contains(text, "level=ERROR") || !strings.Contains(text, "statement timeout") {
		t.Fatalf("logs %s", text)
	}
	if points(t, reader, "live.snapshot.duration", "error.type", "*live.Error") != 1 {
		t.Error("no failed live.snapshot.duration point")
	}
	load := func(context.Context, live.Key, int) ([]note, error) { return nil, nil }
	guarded := live.Snapshot(h, owned("notes"), load)
	problem(t, get(guarded, "/api/live/notes"), http.StatusUnauthorized, "A signed-in user is required.", "")
	forbidden := live.Snapshot(h, func(*http.Request) (live.Key, error) { return "", live.ErrForbidden }, load)
	problem(t, get(forbidden, "/api/live/notes"), http.StatusForbidden, "Your role cannot read this feed.", "")
	if rec := get(guarded, "/api/live/notes", livetest.WithUser(auth.User{ID: 9})); rec.Code != http.StatusOK {
		t.Fatalf("owner %d %s", rec.Code, rec.Body)
	}
	for name, call := range map[string]func(){
		"nil hub":  func() { live.Snapshot(nil, shared("notes"), load) },
		"nil key":  func() { live.Snapshot(h, nil, load) },
		"nil load": func() { live.Snapshot[note](h, shared("notes"), nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s did not panic", name)
				}
			}()
			call()
		}()
	}
}
