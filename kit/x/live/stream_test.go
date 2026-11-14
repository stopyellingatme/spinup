package live_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stopyellingatme/spinup/kit/app"
	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/config"
	"github.com/stopyellingatme/spinup/kit/testkit"
	"github.com/stopyellingatme/spinup/kit/x/live"
	"github.com/stopyellingatme/spinup/kit/x/live/livetest"
)

// The helpers of the external tests. The tests use only the exported API and
// livetest, as an app does.

const (
	wait      = 5 * time.Second
	liveMigr  = "../../../internal/templates/packs/live/files/api/db/kit/00011_live.sql"
	cursorTry = "The since cursor is not valid. Try: pass the cursor of the snapshot, such as 1731500000000-42, or omit since."
)

// streamDB returns a pool on a new database with the live migration. It skips
// the test when DATABASE_URL is empty.
func streamDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL is empty")
	}
	data, err := os.ReadFile(liveMigr)
	if err != nil {
		t.Fatal(err)
	}
	kit := fstest.MapFS{"00011_live.sql": &fstest.MapFile{Data: data, Mode: 0o644}}
	return testkit.DBWithMigrations(t, fstest.MapFS{}, testkit.WithKitMigrations(kit))
}

// put publishes one upsert per ID in one transaction for each ID.
func put(t testing.TB, pool *pgxpool.Pool, key live.Key, ids ...int) {
	t.Helper()
	for _, id := range ids {
		publishTx(t, pool, key, live.Upsert(strconv.Itoa(id), map[string]any{"id": id, "key": string(key)}))
	}
}

func publishTx(t testing.TB, pool *pgxpool.Pool, key live.Key, changes ...live.Change) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Error(err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := live.Publish(ctx, tx, key, changes...); err != nil {
		t.Error(err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		t.Error(err)
	}
}

// epochOf returns the epoch of the feed row of "notes".
func epochOf(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var epoch int64
	if err := pool.QueryRow(t.Context(), `SELECT epoch FROM live_feeds WHERE key = 'notes'`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}

func shared(feed string) live.KeyFunc {
	return func(*http.Request) (live.Key, error) { return live.SharedKey(feed), nil }
}

func owned(feed string) live.KeyFunc {
	return func(r *http.Request) (live.Key, error) {
		user, ok := auth.UserFrom(r.Context())
		if !ok {
			return "", live.ErrUnauthorized
		}
		return live.OwnedKey(feed, user.ID), nil
	}
}

// payload is the data of an event.
type payload struct {
	Epoch   int64  `json:"epoch"`
	Seq     int64  `json:"seq"`
	Resumed bool   `json:"resumed"`
	Reason  string `json:"reason"`
	Changes []struct {
		Op  string          `json:"op"`
		ID  string          `json:"id"`
		Row json.RawMessage `json:"row"`
	} `json:"changes"`
}

// next reads the next event, skips heartbeats, and checks its name.
func next(t testing.TB, s *livetest.Stream, name string) (livetest.Event, payload) {
	t.Helper()
	for {
		ev, err := s.Next(wait)
		if err != nil {
			t.Fatalf("waiting for %s: %v", name, err)
		}
		if ev.Name == "" {
			continue
		}
		if ev.Name != name {
			t.Fatalf("event %q %q %q, want %s", ev.Name, ev.ID, ev.Data, name)
		}
		var p payload
		if err := json.Unmarshal([]byte(ev.Data), &p); err != nil {
			t.Fatalf("event data %q: %v", ev.Data, err)
		}
		return ev, p
	}
}

// patchSeqs reads n patch events and returns their sequence numbers.
func patchSeqs(t testing.TB, s *livetest.Stream, n int) []int64 {
	t.Helper()
	seqs := make([]int64, 0, n)
	for range n {
		ev, p := next(t, s, "patch")
		if ev.ID != fmt.Sprintf("%d-%d", p.Epoch, p.Seq) {
			t.Fatalf("patch id %q, data %q", ev.ID, ev.Data)
		}
		seqs = append(seqs, p.Seq)
	}
	return seqs
}

// end checks that the stream ended: io.EOF after heartbeats.
func end(t testing.TB, s *livetest.Stream) {
	t.Helper()
	for {
		ev, err := s.Next(wait)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil || ev.Name != "" {
			t.Fatalf("want the end of the stream, got %+v %v", ev, err)
		}
	}
}

func equal(a, b []int64) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

// meter returns a meter provider option and its reader.
func meter(t *testing.T) (live.Option, *sdkmetric.ManualReader) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	return live.WithMeterProvider(mp), reader
}

// points returns the sum of an Int64 counter, or the count of a histogram,
// over the data points that have the attribute key=value (any when key is "").
func points(t *testing.T, reader *sdkmetric.ManualReader, name, key, value string) int64 {
	t.Helper()
	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, sm := range data.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, p := range d.DataPoints {
					if v, ok := p.Attributes.Value(attribute.Key(key)); key == "" || ok && v.AsString() == value {
						total += p.Value
					}
				}
			case metricdata.Histogram[float64]:
				for _, p := range d.DataPoints {
					if v, ok := p.Attributes.Value(attribute.Key(key)); key == "" || ok && v.AsString() == value {
						total += int64(p.Count)
					}
				}
			}
		}
	}
	return total
}

// eventually fails the test when cond is not true in 5 seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s: %s", wait, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitHead waits until the hub has the feed at seq: a stream at the head
// reports the head in its ready event.
func waitHead(t *testing.T, h http.Handler, seq int64, opts ...livetest.RequestOption) {
	t.Helper()
	eventually(t, fmt.Sprintf("the hub reaches seq %d", seq), func() bool {
		s := livetest.Open(t, h, "/api/live/notes/live", opts...)
		defer s.Close()
		_, p := next(t, s, "ready")
		return p.Seq >= seq
	})
}

// problem checks a problem answer.
func problem(t *testing.T, rec *httptest.ResponseRecorder, status int, detail, retryAfter string) {
	t.Helper()
	if rec.Code != status || rec.Header().Get("Content-Type") != "application/problem+json" ||
		!strings.Contains(rec.Body.String(), detail) || rec.Header().Get("Retry-After") != retryAfter {
		t.Fatalf("answer %d %q Retry-After %q: %s; want %d %q %q", rec.Code, rec.Header().Get("Content-Type"),
			rec.Header().Get("Retry-After"), rec.Body.String(), status, detail, retryAfter)
	}
}

// get serves one request with a recorder. It suits answers that end at once.
func get(h http.Handler, target string, opts ...livetest.RequestOption) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	for _, opt := range opts {
		opt(r)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestStreamStartsAtTheHead(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	put(t, pool, "notes", 1)
	epoch := epochOf(t, pool)
	h := livetest.Hub(t, pool)
	s := livetest.Open(t, h.Stream(shared("notes")), "/api/live/notes/live")
	if s.Status() != http.StatusOK {
		t.Fatal(s.Status())
	}
	header := s.Header()
	if header.Get("Content-Type") != "text/event-stream" || header.Get("Cache-Control") != "no-cache, no-transform" || header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("header %v", header)
	}
	ev, p := next(t, s, "ready")
	if ev.ID != fmt.Sprintf("%d-1", epoch) || p.Resumed || p.Epoch != epoch || p.Seq != 1 {
		t.Fatalf("ready %+v %+v", ev, p)
	}
	put(t, pool, "notes", 7)
	ev, p = next(t, s, "patch")
	if ev.ID != fmt.Sprintf("%d-2", epoch) || len(p.Changes) != 1 || p.Changes[0].Op != "upsert" || p.Changes[0].ID != "7" {
		t.Fatalf("patch %+v", ev)
	}
}

func TestStreamResume(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	put(t, pool, "notes", 1, 2, 3, 4, 5)
	epoch := epochOf(t, pool)
	h := livetest.Hub(t, pool)
	stream := h.Stream(shared("notes"))

	s := livetest.Open(t, stream, fmt.Sprintf("/api/live/notes/live?since=%d-2", epoch))
	if ev, p := next(t, s, "ready"); !p.Resumed || p.Seq != 5 || ev.ID != fmt.Sprintf("%d-5", epoch) {
		t.Fatalf("ready %+v", ev)
	}
	if got := patchSeqs(t, s, 3); !equal(got, []int64{3, 4, 5}) {
		t.Fatalf("replay %v", got)
	}
	put(t, pool, "notes", 6)
	if got := patchSeqs(t, s, 1); !equal(got, []int64{6}) {
		t.Fatalf("live %v", got)
	}

	s = livetest.Open(t, stream, "/api/live/notes/live", livetest.WithHeader("Last-Event-ID", fmt.Sprintf("%d-3", epoch)))
	if _, p := next(t, s, "ready"); !p.Resumed {
		t.Fatal("Last-Event-ID did not resume")
	}
	if got := patchSeqs(t, s, 3); !equal(got, []int64{4, 5, 6}) {
		t.Fatalf("Last-Event-ID replay %v", got)
	}

	s = livetest.Open(t, stream, fmt.Sprintf("/api/live/notes/live?since=%d-4", epoch), livetest.WithHeader("Last-Event-ID", fmt.Sprintf("%d-1", epoch)))
	next(t, s, "ready")
	if got := patchSeqs(t, s, 2); !equal(got, []int64{5, 6}) {
		t.Fatalf("since must win over Last-Event-ID: %v", got)
	}

	// A cursor at the head replays nothing and gets the next patch.
	s = livetest.Open(t, stream, fmt.Sprintf("/api/live/notes/live?since=%d-6", epoch))
	if _, p := next(t, s, "ready"); !p.Resumed || p.Seq != 6 {
		t.Fatalf("ready %+v", p)
	}
	put(t, pool, "notes", 7)
	if got := patchSeqs(t, s, 1); !equal(got, []int64{7}) {
		t.Fatalf("after the head %v", got)
	}
}

func TestStreamZeroCursor(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	h := livetest.Hub(t, pool)
	stream := h.Stream(shared("notes"))

	// A feed with no row: 0-0 starts, and the first patch arrives as seq 1.
	s := livetest.Open(t, stream, "/api/live/notes/live?since=0-0")
	if ev, p := next(t, s, "ready"); ev.ID != "0-0" || !p.Resumed {
		t.Fatalf("ready %+v", ev)
	}
	put(t, pool, "notes", 1)
	if got := patchSeqs(t, s, 1); !equal(got, []int64{1}) {
		t.Fatalf("first patch %v", got)
	}

	// A feed with rows: 0-0 replays from seq 1.
	put(t, pool, "notes", 2)
	s = livetest.Open(t, stream, "/api/live/notes/live?since=0-0")
	if _, p := next(t, s, "ready"); !p.Resumed || p.Seq < 1 {
		t.Fatalf("ready %+v", p)
	}
	if got := patchSeqs(t, s, 2); !equal(got, []int64{1, 2}) {
		t.Fatalf("replay %v", got)
	}

	// A cursor with an epoch on a feed with no row is an old epoch.
	s = livetest.Open(t, h.Stream(shared("other")), "/api/live/other/live?since=17-3")
	if _, p := next(t, s, "sync"); p.Reason != "epoch" {
		t.Fatalf("sync %+v", p)
	}
	end(t, s)
}

func TestStreamSyncReasons(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	put(t, pool, "notes", 1, 2, 3, 4, 5)
	epoch := epochOf(t, pool)
	opt, reader := meter(t)
	h := livetest.Hub(t, pool, opt)
	stream := h.Stream(shared("notes"))
	for _, tc := range []struct{ since, reason string }{
		{fmt.Sprintf("%d-2", epoch+1), "epoch"},
		{fmt.Sprintf("%d-99", epoch), "ahead"},
	} {
		s := livetest.Open(t, stream, "/api/live/notes/live?since="+tc.since)
		ev, p := next(t, s, "sync")
		if p.Reason != tc.reason || p.Epoch != epoch || p.Seq != 5 || ev.ID != "" {
			t.Fatalf("since %s: sync %+v %+v", tc.since, ev, p)
		}
		end(t, s)
	}

	// gap: the outbox lost the old patches, and a new hub loads the feed.
	if _, err := live.Prune(t.Context(), pool, live.PruneOptions{Patches: time.Nanosecond}); err != nil {
		t.Fatal(err)
	}
	fresh := livetest.Hub(t, pool).Stream(shared("notes"))
	s := livetest.Open(t, fresh, fmt.Sprintf("/api/live/notes/live?since=%d-2", epoch))
	if _, p := next(t, s, "sync"); p.Reason != "gap" {
		t.Fatalf("sync %+v", p)
	}
	end(t, s)
	// The head still works after the gap.
	s = livetest.Open(t, fresh, fmt.Sprintf("/api/live/notes/live?since=%d-5", epoch))
	next(t, s, "ready")
	put(t, pool, "notes", 6)
	if got := patchSeqs(t, s, 1); !equal(got, []int64{6}) {
		t.Fatalf("after gap %v", got)
	}

	// reset: an operator gives the feed a new epoch, and NOTIFY tells the hub.
	waitHead(t, stream, 6)
	s = livetest.Open(t, stream, "/api/live/notes/live")
	next(t, s, "ready")
	if _, err := pool.Exec(t.Context(), `UPDATE live_feeds SET epoch = epoch + 1, seq = 0 WHERE key = 'notes'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `SELECT pg_notify('spinup_live', '0 notes')`); err != nil {
		t.Fatal(err)
	}
	if _, p := next(t, s, "sync"); p.Reason != "reset" || p.Epoch != epoch+1 || p.Seq != 0 {
		t.Fatalf("sync %+v", p)
	}
	end(t, s)
	for _, reason := range []string{"epoch", "ahead", "reset"} {
		if points(t, reader, "live.sync", "live.reason", reason) != 1 {
			t.Errorf("live.sync %s is not 1", reason)
		}
	}
	if n := points(t, reader, "live.stream.duration", "live.end", "sync"); n != 3 {
		t.Errorf("live.stream.duration end=sync: %d", n)
	}
}

func TestStreamSlowReader(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	opt, reader := meter(t)
	h := livetest.Hub(t, pool, opt, live.WithRing(64, 8<<20), live.WithHeartbeat(time.Hour))
	stream := h.Stream(shared("notes"))
	s := livetest.Open(t, stream, "/api/live/notes/live")
	next(t, s, "ready")
	// The reader stops while 2,000 patches arrive.
	ctx := t.Context()
	for batch := range 20 {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for i := range 100 {
			if err := live.Publish(ctx, tx, "notes", live.Touch(strconv.Itoa(batch*100+i))); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	waitHead(t, stream, 2000)
	var seqs []int64
	for {
		ev, p := func() (livetest.Event, payload) {
			ev, err := s.Next(wait)
			if err != nil {
				t.Fatal(err)
			}
			var p payload
			_ = json.Unmarshal([]byte(ev.Data), &p)
			return ev, p
		}()
		if ev.Name == "sync" {
			if p.Reason != "slow" {
				t.Fatalf("sync %+v", p)
			}
			break
		}
		if ev.Name != "patch" || p.Seq != int64(len(seqs)+1) {
			t.Fatalf("event %+v after %d patches", ev, len(seqs))
		}
		seqs = append(seqs, p.Seq)
	}
	end(t, s)
	if len(seqs) >= 2000 {
		t.Fatalf("the slow reader got all %d patches", len(seqs))
	}
	if points(t, reader, "live.sync", "live.reason", "slow") != 1 {
		t.Error("live.sync slow is not 1")
	}
}

func TestStreamRefusals(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	logs := &lockedBuffer{}
	opt, reader := meter(t)
	h := livetest.Hub(t, pool, opt, live.WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	stream := h.Stream(shared("notes"))
	for _, since := range []string{"", "x", "1", "1-", "-1", "1-2-3", "+1-2", "1-+2", "1234567890123456789-1", "1-1234567890123456789", "1--2", " 1-2"} {
		problem(t, get(stream, "/api/live/notes/live?since="+urlQuery(since)), http.StatusBadRequest, cursorTry, "")
	}
	problem(t, get(stream, "/api/live/notes/live", livetest.WithHeader("Last-Event-ID", "nope")), http.StatusBadRequest, cursorTry, "")
	if n := points(t, reader, "live.stream.opened", "live.result", "bad_cursor"); n != 13 {
		t.Errorf("bad_cursor count %d", n)
	}

	refuse := func(err error) http.Handler {
		return h.Stream(func(*http.Request) (live.Key, error) { return "", err })
	}
	problem(t, get(refuse(live.ErrUnauthorized), "/api/live/notes/live"), http.StatusUnauthorized, "A signed-in user is required. Try: sign in, then connect again.", "")
	problem(t, get(refuse(fmt.Errorf("wrapped: %w", live.ErrForbidden)), "/api/live/notes/live"), http.StatusForbidden, "Your role cannot read this feed. Try: sign in with an account that has a required role.", "")
	problem(t, get(refuse(errors.New("role table is gone")), "/api/live/notes/live"), http.StatusInternalServerError, "The feed could not start. Try: retry the request later.", "")
	problem(t, get(h.Stream(shared("Bad Feed")), "/api/live/notes/live"), http.StatusInternalServerError, "The feed could not start. Try: retry the request later.", "")
	if text := logs.String(); !strings.Contains(text, "level=ERROR") || !strings.Contains(text, "role table is gone") || !strings.Contains(text, "Bad Feed") {
		t.Fatalf("logs %s", text)
	}
	for result, want := range map[string]int64{"unauthorized": 1, "forbidden": 1, "error": 2} {
		if n := points(t, reader, "live.stream.opened", "live.result", result); n != want {
			t.Errorf("result %s: %d, want %d", result, n, want)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("Stream(nil) did not panic")
		}
	}()
	h.Stream(nil)
}

func urlQuery(s string) string {
	return strings.NewReplacer("+", "%2B", " ", "%20").Replace(s)
}

// lockedBuffer is a log destination for concurrent writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestStreamHeartbeat(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	h := livetest.Hub(t, pool, live.WithHeartbeat(50*time.Millisecond))
	s := livetest.Open(t, h.Stream(shared("notes")), "/api/live/notes/live")
	next(t, s, "ready")
	ev, err := s.Next(wait)
	if err != nil || ev != (livetest.Event{Data: "ping"}) {
		t.Fatalf("heartbeat %+v %v", ev, err)
	}
}

func TestStreamAgeEndsWithBye(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	put(t, pool, "notes", 1)
	opt, reader := meter(t)
	h := livetest.Hub(t, pool, opt, live.WithMaxStreamAge(200*time.Millisecond))
	stream := h.Stream(shared("notes"))
	start := time.Now()
	s := livetest.Open(t, stream, "/api/live/notes/live")
	ready, _ := next(t, s, "ready")
	ev, _ := next(t, s, "bye")
	if ev.Data != `{"reason":"age"}` || ev.ID != "" {
		t.Fatalf("bye %+v", ev)
	}
	if age := time.Since(start); age < 160*time.Millisecond {
		t.Fatalf("bye after %s, want at least 80 percent of 200ms", age)
	}
	end(t, s)
	if points(t, reader, "live.stream.duration", "live.end", "age") != 1 {
		t.Error("no live.stream.duration with live.end=age")
	}
	// The cursor stays good after bye.
	s = livetest.Open(t, stream, "/api/live/notes/live?since="+ready.ID)
	if _, p := next(t, s, "ready"); !p.Resumed || p.Seq != 1 {
		t.Fatalf("resume after bye %+v", p)
	}
}

func TestStreamWriteTimeout(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	opt, reader := meter(t)
	h := livetest.Hub(t, pool, opt, live.WithWriteTimeout(100*time.Millisecond), live.WithHeartbeat(time.Hour))
	s := livetest.Open(t, h.Stream(shared("notes")), "/api/live/notes/live")
	next(t, s, "ready")
	// The test stops reading: the second write of the handler waits on the pipe.
	put(t, pool, "notes", 1, 2, 3)
	eventually(t, "the stream ends with live.end=write", func() bool {
		return points(t, reader, "live.stream.duration", "live.end", "write") == 1 &&
			points(t, reader, "live.stream.active", "", "") == 0
	})
	for {
		ev, err := s.Next(wait)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatal("the stream did not end")
			}
			break
		}
		if ev.Name != "patch" {
			t.Fatalf("event %+v", ev)
		}
	}
}

func TestStreamOwnerScope(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	h := livetest.Hub(t, pool, live.WithHeartbeat(time.Hour))
	stream := h.Stream(owned("notes"))
	a := livetest.Open(t, stream, "/api/live/notes/live", livetest.WithUser(auth.User{ID: 1}))
	b := livetest.Open(t, stream, "/api/live/notes/live", livetest.WithUser(auth.User{ID: 2}))
	next(t, a, "ready")
	next(t, b, "ready")
	put(t, pool, live.OwnedKey("notes", 1), 10, 11)
	put(t, pool, live.OwnedKey("notes", 2), 20)
	for _, want := range []string{"10", "11"} {
		if _, p := next(t, a, "patch"); p.Changes[0].ID != want || !strings.Contains(string(p.Changes[0].Row), `"notes:1"`) {
			t.Fatalf("user A got %+v", p)
		}
	}
	if _, p := next(t, b, "patch"); p.Seq != 1 || p.Changes[0].ID != "20" || !strings.Contains(string(p.Changes[0].Row), `"notes:2"`) {
		t.Fatalf("user B got %+v", p)
	}
	// User B never gets a patch of user A.
	put(t, pool, live.OwnedKey("notes", 1), 12)
	next(t, a, "patch")
	for {
		ev, err := b.Next(300 * time.Millisecond)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			break
		}
		if err != nil || ev.Name != "" {
			t.Fatalf("user B got %+v %v", ev, err)
		}
	}
	problem(t, get(stream, "/api/live/notes/live"), http.StatusUnauthorized, "A signed-in user is required.", "")
}

func TestStreamBurst(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	h := livetest.Hub(t, pool, live.WithHeartbeat(time.Hour))
	stream := h.Stream(shared("notes"))
	const streams, writers, each = 20, 4, 50
	open := make([]*livetest.Stream, streams)
	for i := range open {
		open[i] = livetest.Open(t, stream, "/api/live/notes/live", livetest.WithUser(auth.User{ID: int64(i + 1)}))
		next(t, open[i], "ready")
	}
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range each {
				put(t, pool, "notes", w*each+i)
			}
		})
	}
	for i, s := range open {
		wg.Go(func() {
			seen := map[string]bool{}
			for want := int64(1); want <= writers*each; want++ {
				ev, err := s.Next(wait)
				if err != nil {
					t.Errorf("stream %d after seq %d: %v", i, want-1, err)
					return
				}
				var p payload
				if err := json.Unmarshal([]byte(ev.Data), &p); err != nil || ev.Name != "patch" || p.Seq != want || seen[p.Changes[0].ID] {
					t.Errorf("stream %d: event %+v, want seq %d", i, ev, want)
					return
				}
				seen[p.Changes[0].ID] = true
			}
		})
	}
	wg.Wait()
}

// TestStreamHTTP runs the stream behind a real HTTP server and reads the bytes.
func TestStreamHTTP(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	put(t, pool, "notes", 1)
	epoch := epochOf(t, pool)
	h := livetest.Hub(t, pool)
	server := httptest.NewServer(h.Stream(shared("notes")))
	t.Cleanup(server.Close)

	resp, lines := sseGet(t, server.URL+fmt.Sprintf("/api/live/notes/live?since=%d-0", epoch))
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" ||
		resp.Header.Get("Cache-Control") != "no-cache, no-transform" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("answer %d %v", resp.StatusCode, resp.Header)
	}
	want := func(prefix string) {
		t.Helper()
		select {
		case line, ok := <-lines:
			if !ok || !strings.HasPrefix(line, prefix) {
				t.Fatalf("line %q (open %v), want the prefix %q", line, ok, prefix)
			}
		case <-time.After(wait):
			t.Fatalf("no line with the prefix %q", prefix)
		}
	}
	want("event: ready")
	want(fmt.Sprintf("id: %d-1", epoch))
	want(fmt.Sprintf(`data: {"epoch":%d,"seq":1,"resumed":true}`, epoch))
	want("")
	want("event: patch")
	want(fmt.Sprintf("id: %d-1", epoch))
	want(fmt.Sprintf(`data: {"epoch":%d,"seq":1,"changes":[{"id": "1", "op": "upsert", "row": {"id": 1, "key": "notes"}}]}`, epoch))
	want("")
	want(": ping")
	want("")
	put(t, pool, "notes", 2)
	for {
		line := <-lines
		if line == "event: patch" {
			break
		}
		if line != ": ping" && line != "" {
			t.Fatalf("line %q", line)
		}
	}
	want(fmt.Sprintf("id: %d-2", epoch))

	start := time.Now()
	if err := h.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Drain took %s", d)
	}
	var rest []string
	for line := range lines {
		rest = append(rest, line)
	}
	if text := strings.Join(rest, "\n"); !strings.HasSuffix(text, "event: bye\ndata: {\"reason\":\"drain\"}\n") {
		t.Fatalf("end of the stream %q", text)
	}
}

// sseAnswer is the status and the header of a stream answer.
type sseAnswer struct {
	StatusCode int
	Header     http.Header
}

// sseGet sends a GET request and returns the answer and a channel of the body
// lines. The channel closes at the end of the body.
func sseGet(t *testing.T, url string) (sseAnswer, <-chan string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // readLines closes the body
	if err != nil {
		t.Fatal(err)
	}
	answer := sseAnswer{StatusCode: resp.StatusCode, Header: resp.Header}
	lines := make(chan string, 64)
	go readLines(ctx, resp, lines)
	return answer, lines
}

// readLines sends the body lines of resp to lines until the body or ctx ends.
func readLines(ctx context.Context, resp *http.Response, lines chan<- string) {
	defer close(lines)
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		select {
		case lines <- scanner.Text():
		case <-ctx.Done():
			return
		}
	}
}

// TestStreamThroughApp runs a stream through the full kit/app chain in dev
// mode: the request deadline exemption with IsStream, the devtools writer,
// and WithDrain at shutdown.
func TestStreamThroughApp(t *testing.T) {
	t.Parallel()
	pool := streamDB(t)
	h := livetest.Hub(t, pool)
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener is not available: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	a := app.New(
		app.WithAddr(addr),
		app.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		app.WithConfig(config.Base{Env: "dev", ShutdownTimeout: 20 * time.Second}),
		app.WithRequestTimeout(150*time.Millisecond),
		app.WithRequestTimeoutExempt(live.IsStream),
		app.WithRoutes(func(mux *http.ServeMux) { mux.Handle("GET /api/live/notes/live", h.Stream(shared("notes"))) }),
		app.WithDrain(h.Drain),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, nil) }()
	eventually(t, "the app serves /healthz", func() bool {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/healthz", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})

	resp, lines := sseGet(t, "http://"+addr+"/api/live/notes/live")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-cache, no-transform" {
		t.Fatalf("answer %d %v", resp.StatusCode, resp.Header)
	}
	// Three heartbeats take more than the request timeout of 150ms. The devtools
	// writer passes each flush through, so each frame arrives on its own.
	pings := 0
	for pings < 3 {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("the stream ended early")
			}
			if line == ": ping" {
				pings++
			}
		case <-time.After(wait):
			t.Fatal("no heartbeat")
		}
	}
	put(t, pool, "notes", 1)
	for line := range lines {
		if line == "event: patch" {
			break
		}
	}

	start := time.Now()
	cancel()
	var rest []string
	for line := range lines {
		rest = append(rest, line)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(wait):
		t.Fatal("Run did not return")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("shutdown took %s with an open stream", d)
	}
	if text := strings.Join(rest, "\n"); !strings.Contains(text, "event: bye\ndata: {\"reason\":\"drain\"}") {
		t.Fatalf("end of the stream %q", text)
	}
}
