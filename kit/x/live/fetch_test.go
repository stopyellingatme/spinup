package live

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDirtySetRunsAFeedAgainAfterAMarkDuringItsFetch(t *testing.T) {
	t.Parallel()
	h := &Hub{fetchState: map[Key]fetchMark{}, notified: map[Key]int64{}, work: make(chan struct{}, 1)}
	h.mark("a")
	h.mark("b")
	h.mark("a")
	if !slices.Equal(h.pending, []Key{"a", "b"}) {
		t.Fatalf("pending = %v, want [a b]", h.pending)
	}
	k, ok := h.next()
	if !ok || k != "a" || h.fetchState["a"] != markRunning {
		t.Fatalf("next = %q, %v; state %d", k, ok, h.fetchState["a"])
	}
	// A mark during the fetch does not start a second fetch of the feed.
	h.mark("a")
	if !slices.Equal(h.pending, []Key{"b"}) || h.fetchState["a"] != markRerun {
		t.Fatalf("pending = %v, state %d; want [b] and markRerun", h.pending, h.fetchState["a"])
	}
	h.done("a")
	if !slices.Equal(h.pending, []Key{"b", "a"}) || h.fetchState["a"] != markQueued {
		t.Fatalf("after done: pending = %v, state %d", h.pending, h.fetchState["a"])
	}
	for range 2 {
		k, _ := h.next()
		h.done(k)
	}
	if len(h.pending) != 0 || len(h.fetchState) != 0 {
		t.Fatalf("the dirty set is not empty: %v %v", h.pending, h.fetchState)
	}
	if _, ok := h.next(); ok {
		t.Fatal("next on an empty set is ok")
	}
}

func TestLoadKeepsTheLastPatches(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	exec(t, pool, `INSERT INTO live_feeds (key, seq) VALUES ('big', 3000)`)
	exec(t, pool, `INSERT INTO live_outbox (feed, seq, changes) SELECT 'big', g, '[{"op":"touch","id":"1"}]' FROM generate_series(1, 3000) g`)
	h := newHub(t, pool)
	f := h.acquireFeed(t, "big")
	row, _ := feedRow(t, pool, "big")
	if f.head() != row {
		t.Fatalf("head = %v, want %v", f.head(), row)
	}
	if f.ring.len() != 1024 || f.ring.first() != 3000-1023 || f.ring.last() != 3000 {
		t.Fatalf("ring holds %d patches from %d to %d, want 1024 from 1977 to 3000", f.ring.len(), f.ring.first(), f.ring.last())
	}
	if h.totalRingBytes() != f.ring.bytes() || f.ring.bytes() == 0 {
		t.Fatalf("ring bytes = %d, hub sum = %d", f.ring.bytes(), h.totalRingBytes())
	}
	s := f.subscribe(2999)
	defer f.unsubscribe(s)
	frames, state := readAll(f, s)
	want := patchFrame(row.Epoch, 2999, []byte(`[{"id": "1", "op": "touch"}]`))
	if state != readOK || len(frames) != 2 || string(frames[0]) != string(want) {
		t.Fatalf("read = %q, state %d; want 2 frames from %q", frames, state, want)
	}
	// A second stream of the feed finds it loaded.
	g, err := h.acquire(t.Context(), "big")
	if err != nil || g != f {
		t.Fatalf("second acquire = %p, %v; want %p", g, err, f)
	}
	h.release(g)
}

func TestLoadStopsAtAGap(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	exec(t, pool, `INSERT INTO live_feeds (key, seq) VALUES ('gappy', 10)`)
	exec(t, pool, `INSERT INTO live_outbox (feed, seq, changes) SELECT 'gappy', g, '[]' FROM generate_series(1, 10) g WHERE g <> 6`)
	h := newHub(t, pool)
	f := h.acquireFeed(t, "gappy")
	if f.head().Seq != 10 || f.ring.first() != 7 || f.ring.last() != 10 {
		t.Fatalf("head %v, ring %d..%d; want head 10 and ring 7..10", f.head(), f.ring.first(), f.ring.last())
	}
}

func TestEmptyFeedTakesTheFirstEpochWithNoReset(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	h := startHub(t, pool, WithPollInterval(time.Hour))
	f := h.acquireFeed(t, "notes:9")
	if f.head() != (Cursor{}) || f.ring.len() != 0 {
		t.Fatalf("feed with no row: head %v, ring %d", f.head(), f.ring.len())
	}
	s := f.subscribe(1)
	defer f.unsubscribe(s)
	publish(t, pool, "notes:9", Touch("1"))
	waitWake(t, s, 5*time.Second)
	frames, state := readAll(f, s)
	if state != readOK || len(frames) != 1 {
		t.Fatalf("read = %d frames, state %d; want 1 frame and no reset", len(frames), state)
	}
	row, _ := feedRow(t, pool, "notes:9")
	if f.head() != row || row.Epoch == 0 {
		t.Fatalf("head = %v, want %v", f.head(), row)
	}
}

func TestEpochChangeResetsTheFeed(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	publish(t, pool, "notes", Touch("1"))
	publish(t, pool, "notes", Touch("2"))
	h := startHub(t, pool, WithPollInterval(time.Hour))
	f := h.acquireFeed(t, "notes")
	h.settle(t)
	before := f.head()
	s := f.subscribe(3)
	defer f.unsubscribe(s)
	exec(t, pool, `UPDATE live_feeds SET epoch = epoch + 1 WHERE key = 'notes'`)
	exec(t, pool, `SELECT pg_notify('spinup_live', '2 notes')`)
	waitWake(t, s, 5*time.Second)
	if _, state := readAll(f, s); state != readReset {
		t.Fatalf("read state = %d, want readReset", state)
	}
	h.settle(t)
	if got := f.head(); got.Epoch != before.Epoch+1 || got.Seq != 2 {
		t.Fatalf("head after reset = %v, want epoch %d and seq 2", got, before.Epoch+1)
	}
	if f.ring.len() != 2 {
		t.Fatalf("the ring holds %d patches after the reset, want 2 (loaded again)", f.ring.len())
	}
	if !strings.Contains(h.logs.String(), "a feed was reset") {
		t.Fatalf("no log line for the reset:\n%s", h.logs)
	}
}

func TestMissingRowResetsTheFeed(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	publish(t, pool, "notes", Touch("1"))
	publish(t, pool, "notes", Touch("2"))
	h := startHub(t, pool, WithPollInterval(time.Hour))
	f := h.acquireFeed(t, "notes")
	h.settle(t)
	s := f.subscribe(3)
	defer f.unsubscribe(s)
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for i := range 3 {
		if err := Publish(ctx, tx, "notes", Touch(string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	// The patches 3 and 4 are below the head of the feed (5), and they go.
	if _, err := tx.Exec(ctx, `DELETE FROM live_outbox WHERE feed = 'notes' AND seq IN (3, 4)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	waitWake(t, s, 5*time.Second)
	if _, state := readAll(f, s); state != readReset {
		t.Fatalf("read state = %d, want readReset", state)
	}
	h.settle(t)
	if got := f.head(); got.Seq != 5 || f.ring.first() != 5 || f.ring.last() != 5 {
		t.Fatalf("head %v, ring %d..%d; want head 5 and ring 5..5", got, f.ring.first(), f.ring.last())
	}
	// The next patch follows on with no reset.
	s2 := f.subscribe(6)
	defer f.unsubscribe(s2)
	publish(t, pool, "notes", Touch("z"))
	waitWake(t, s2, 5*time.Second)
	if frames, state := readAll(f, s2); state != readOK || len(frames) != 1 {
		t.Fatalf("read after the reset = %d frames, state %d", len(frames), state)
	}
}

func TestHubPrunesTheOutbox(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	publish(t, pool, "notes", Touch("1"))
	publish(t, pool, "notes", Touch("2"))
	exec(t, pool, `UPDATE live_outbox SET created_at = now() - interval '2 hours' WHERE seq = 1`)
	startHub(t, pool, func(o *options) { o.pruneEvery = 20 * time.Millisecond })
	eventually(t, "the janitor prunes the old patch", func() bool {
		return count(t, pool, `SELECT count(*) FROM live_outbox`) == 1
	})
	if n := count(t, pool, `SELECT count(*) FROM live_outbox WHERE seq = 2`); n != 1 {
		t.Fatal("the janitor pruned the new patch")
	}
}

// TestResetIsAtomicForAJoiningStream lets a stream try to open while the reset
// holds the feed. The stream must see the full new feed: the new head, a next
// of head+1, and no reset mark. A stream that was open before the reset reads
// readReset, and a stream that resumes from a cursor in the new ring gets the
// patches after it.
func TestResetIsAtomicForAJoiningStream(t *testing.T) {
	t.Parallel()
	type opened struct {
		s       *sub
		head    Cursor
		resumed bool
		reason  string
	}
	joined := make(chan opened, 1)
	h := newHub(t, lazyPool(t), func(o *options) {
		o.duringReset = func(f *feed) {
			go func() {
				s, head, resumed, reason := f.open(Cursor{}, false)
				joined <- opened{s, head, resumed, reason}
			}()
			// The stream waits for the feed lock while the reset holds it.
			time.Sleep(20 * time.Millisecond)
			select {
			case got := <-joined:
				t.Errorf("a stream opened during the reset, with the head %v", got.head)
				joined <- got
			default:
			}
		}
	})
	f := readyFeed("notes", 3, 10)
	h.feeds[f.key] = f
	h.ringBytes = f.ring.bytes()
	before := f.subscribe(4)
	frames := []pendingFrame{
		{seq: 10, frame: patchFrame(2, 10, []byte(`[]`))},
		{seq: 9, frame: patchFrame(2, 9, []byte(`[]`))},
		{seq: 8, frame: patchFrame(2, 8, []byte(`[]`))},
	}
	if !h.applyReset(f, 2, 10, frames) {
		t.Fatal("applyReset = false for a tracked feed")
	}
	var got opened
	select {
	case got = <-joined:
	case <-time.After(5 * time.Second):
		t.Fatal("the joining stream did not open")
	}
	if got.reason != "" || got.head != (Cursor{Epoch: 2, Seq: 10}) || got.s.next != 11 || got.s.reset {
		t.Fatalf("the joining stream: head %v, next %d, reset %v, reason %q; want 2-10, 11, false, none",
			got.head, got.s.next, got.s.reset, got.reason)
	}
	waitWake(t, got.s, time.Second)
	if n, state := readAll(f, got.s); state != readOK || len(n) != 0 {
		t.Fatalf("the first read of the joining stream: %d frames, state %d; want 0 and readOK", len(n), state)
	}
	waitWake(t, before, time.Second)
	if _, state := readAll(f, before); state != readReset {
		t.Fatalf("the stream from before the reset reads %d, want readReset", state)
	}
	resumed, _, ok, reason := f.open(Cursor{Epoch: 2, Seq: 8}, true)
	if !ok || reason != "" {
		t.Fatalf("resume from 2-8: resumed %v, reason %q", ok, reason)
	}
	if out, state := readAll(f, resumed); state != readOK || len(out) != 2 || string(out[1]) != string(frames[0].frame) {
		t.Fatalf("resume from 2-8 read %d frames, state %d; want the patches 9 and 10", len(out), state)
	}
	if h.ringBytes != f.ring.bytes() {
		t.Fatalf("hub ring bytes %d, ring %d", h.ringBytes, f.ring.bytes())
	}
}

// TestLoadPressureTrimForgetsTheEndedFeeds checks that a trim during the load
// of a feed removes the notification marks of the idle feeds that it ends.
func TestLoadPressureTrimForgetsTheEndedFeeds(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	exec(t, pool, `INSERT INTO live_feeds (key, seq) VALUES ('fresh', 20)`)
	exec(t, pool, `INSERT INTO live_outbox (feed, seq, changes) SELECT 'fresh', g, '[{"op":"touch","id":"1"}]' FROM generate_series(1, 20) g`)
	// The 20 frames have about 2400 bytes. With the idle ring the sum is over 3000.
	h := newHub(t, pool, WithRing(64, 3000))
	idle := readyFeed("idle", 10, 150)
	idle.idleSince = time.Now().Add(-time.Second)
	h.feeds[idle.key] = idle
	h.ringBytes = idle.ring.bytes()
	h.notified["idle"] = 10
	f := h.acquireFeed(t, "fresh")
	if f.ring.len() != 20 || f.ring.bytes() > 3000 {
		t.Fatalf("the loaded ring holds %d patches (%d bytes), want 20 in at most 3000 bytes", f.ring.len(), f.ring.bytes())
	}
	h.mu.Lock()
	_, tracked := h.feeds["idle"]
	h.mu.Unlock()
	if tracked {
		t.Fatal("the idle feed stays after the pressure trim of the load")
	}
	h.dmu.Lock()
	_, mark := h.notified["idle"]
	h.dmu.Unlock()
	if mark {
		t.Fatal("the mark of the feed that the load trim ended stays")
	}
}
