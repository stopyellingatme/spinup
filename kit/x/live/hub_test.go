package live

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// lazyPool returns a pool that never connects. New does no I/O, so the tests
// of options and counts need no database.
func lazyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestNewValidates(t *testing.T) {
	t.Parallel()
	var le *Error
	if _, err := New(nil); !errors.As(err, &le) || !strings.Contains(err.Error(), "the pool is nil") || le.Try == "" {
		t.Fatalf("New(nil) = %v", err)
	}
	pool := lazyPool(t)
	for _, opt := range []Option{WithMaxStreamAge(-1), WithHeartbeat(-1), WithPollInterval(-time.Second), WithBootstrapWait(-1), WithRetention(-1), WithIdleTTL(-1), WithWriteTimeout(-1)} {
		_, err := New(pool, opt)
		if !errors.As(err, &le) || !strings.Contains(err.Error(), "negative duration") || !strings.Contains(err.Error(), "\nTry: ") {
			t.Fatalf("New with a negative duration = %v", err)
		}
	}
	h, err := New(pool, WithMaxStreams(-5), WithRing(0, 0), WithSnapshotLimit(0), nil)
	if err != nil {
		t.Fatal(err)
	}
	o := h.opts
	if o.maxStreams != 2000 || o.maxStreamsPerUser != 8 || o.maxStreamAge != 15*time.Minute || o.heartbeat != 15*time.Second ||
		o.pollInterval != 5*time.Second || o.ringPatches != 1024 || o.ringBytes != 8<<20 || o.bootstrapLimit != 4 ||
		o.bootstrapWait != 2*time.Second || o.retention != time.Hour || o.idleTTL != 60*time.Second ||
		o.writeTimeout != 10*time.Second || o.snapshotLimit != 500 || o.logger == nil {
		t.Fatalf("defaults = %+v", o)
	}
	if cap(h.boot) != 4 {
		t.Fatalf("bootstrap slots = %d, want 4", cap(h.boot))
	}
	h, err = New(pool, WithMaxStreams(3), WithMaxStreamsPerUser(2), WithMaxStreamAge(time.Minute), WithHeartbeat(time.Second),
		WithPollInterval(time.Second), WithRing(64, 4096), WithBootstrapLimit(2), WithBootstrapWait(time.Millisecond),
		WithRetention(time.Minute), WithIdleTTL(time.Second), WithWriteTimeout(time.Second), WithSnapshotLimit(10))
	if err != nil {
		t.Fatal(err)
	}
	o = h.opts
	if o.maxStreams != 3 || o.maxStreamsPerUser != 2 || o.maxStreamAge != time.Minute || o.heartbeat != time.Second ||
		o.pollInterval != time.Second || o.ringPatches != 64 || o.ringBytes != 4096 || o.bootstrapLimit != 2 ||
		o.bootstrapWait != time.Millisecond || o.retention != time.Minute || o.idleTTL != time.Second ||
		o.writeTimeout != time.Second || o.snapshotLimit != 10 {
		t.Fatalf("options = %+v", o)
	}
}

func TestAdmit(t *testing.T) {
	t.Parallel()
	h := newHub(t, lazyPool(t), WithMaxStreams(3), WithMaxStreamsPerUser(2))
	a1, err := h.admit(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.admit(1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.admit(1); !errors.Is(err, errUserLimit) {
		t.Fatalf("third stream of user 1: err = %v, want errUserLimit", err)
	}
	if _, err := h.admit(2); err != nil {
		t.Fatal(err)
	}
	if _, err := h.admit(3); !errors.Is(err, errGlobalLimit) {
		t.Fatalf("fourth stream: err = %v, want errGlobalLimit", err)
	}
	// The per-user limit comes first.
	if _, err := h.admit(1); !errors.Is(err, errUserLimit) {
		t.Fatalf("user 1 at both limits: err = %v, want errUserLimit", err)
	}
	a1()
	a1()
	h.mu.Lock()
	streams, user1 := h.streams, h.perUser[1]
	h.mu.Unlock()
	if streams != 2 || user1 != 1 {
		t.Fatalf("after a release called twice: streams %d, user 1 has %d; want 2 and 1", streams, user1)
	}
	a3, err := h.admit(3)
	if err != nil {
		t.Fatalf("after a release: %v", err)
	}
	a3()
	h.mu.Lock()
	_, found := h.perUser[3]
	h.mu.Unlock()
	if found {
		t.Fatal("a user with no stream stays in the map")
	}
}

func TestSlot(t *testing.T) {
	t.Parallel()
	h := newHub(t, lazyPool(t), WithBootstrapWait(100*time.Millisecond))
	var releases []func()
	for range 4 {
		r, err := h.slot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, r)
	}
	start := time.Now()
	if _, err := h.slot(t.Context()); !errors.Is(err, errBusy) {
		t.Fatalf("fifth slot: err = %v, want errBusy", err)
	}
	if waited := time.Since(start); waited < 100*time.Millisecond {
		t.Fatalf("the fifth slot waited %s, want BootstrapWait (100ms)", waited)
	}
	if got := h.counter(t, "live.bootstrap.rejected"); got != 1 {
		t.Fatalf("live.bootstrap.rejected = %d, want 1", got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := h.slot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("slot with a canceled context: err = %v", err)
	}
	// A release that runs twice frees one slot.
	releases[0]()
	releases[0]()
	r, err := h.slot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.slot(t.Context()); !errors.Is(err, errBusy) {
		t.Fatalf("slot after a double release: err = %v, want errBusy", err)
	}
	// A waiting request gets the next free slot.
	done := make(chan error, 1)
	go func() {
		r, err := h.slot(t.Context())
		if err == nil {
			r()
		}
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	r()
	if err := <-done; err != nil {
		t.Fatalf("the waiting request: %v", err)
	}
	for _, r := range releases[1:] {
		r()
	}
}

func TestRelease(t *testing.T) {
	t.Parallel()
	h := newHub(t, lazyPool(t))
	f := newFeed("notes", 8)
	h.feeds["notes"] = f
	f.refs = 2
	h.release(f)
	if f.refs != 1 || !f.idleSince.IsZero() {
		t.Fatalf("refs %d, idle since %v", f.refs, f.idleSince)
	}
	h.release(f)
	if f.refs != 0 || f.idleSince.IsZero() {
		t.Fatalf("refs %d, idle since %v; want 0 and a time", f.refs, f.idleSince)
	}
	h.release(f)
	h.release(nil)
	if f.refs != 0 {
		t.Fatalf("refs = %d after extra releases", f.refs)
	}
}

// readyFeed returns a loaded feed with n patches of size bytes each.
func readyFeed(key Key, n, size int) *feed {
	f := newFeed(key, 64)
	for i := 1; i <= n; i++ {
		f.ring.push(int64(i), make([]byte, size))
	}
	f.epoch, f.seq = 1, int64(n)
	close(f.ready)
	return f
}

func TestSweepIdle(t *testing.T) {
	t.Parallel()
	h := newHub(t, lazyPool(t), WithIdleTTL(time.Minute))
	now := time.Now()
	idle, recent, open, loading := readyFeed("idle", 2, 10), readyFeed("recent", 2, 10), readyFeed("open", 2, 10), newFeed("loading", 8)
	idle.idleSince = now.Add(-2 * time.Minute)
	recent.idleSince = now.Add(-time.Second)
	open.refs = 1
	loading.idleSince = now.Add(-time.Hour)
	for _, f := range []*feed{idle, recent, open, loading} {
		h.feeds[f.key] = f
		h.ringBytes += f.ring.bytes()
	}
	h.notified["idle"] = 2
	h.sweepIdle(now)
	if _, ok := h.feeds["idle"]; ok || len(h.feeds) != 3 {
		t.Fatalf("feeds after the sweep: %v", h.feeds)
	}
	if h.ringBytes != 40 {
		t.Fatalf("ring bytes = %d, want 40", h.ringBytes)
	}
	if _, ok := h.notified["idle"]; ok {
		t.Fatal("the mark of the ended feed stays")
	}
	if !idle.closed {
		t.Fatal("the ended feed is not closed")
	}
}

func TestTrimRings(t *testing.T) {
	t.Parallel()
	h := newHub(t, lazyPool(t), WithRing(64, 6000))
	now := time.Now()
	oldIdle, newIdle := readyFeed("old", 10, 100), readyFeed("new", 10, 100)
	oldIdle.idleSince, newIdle.idleSince = now.Add(-time.Minute), now.Add(-time.Second)
	large, small := readyFeed("large", 60, 100), readyFeed("small", 40, 10)
	large.refs, small.refs = 1, 1
	for _, f := range []*feed{oldIdle, newIdle, large, small} {
		h.feeds[f.key] = f
	}
	// The sum is 1000 + 1000 + 6000 + 400 = 8400 bytes.
	h.trimRings()
	if _, ok := h.feeds["old"]; ok {
		t.Fatal("the feed that has been idle the longest stays")
	}
	if _, ok := h.feeds["new"]; ok {
		t.Fatal("the second idle feed stays")
	}
	// 6400 bytes are left: the largest ring drops 4 patches of 100 bytes.
	if large.ring.len() != 56 || large.ring.first() != 5 || small.ring.len() != 40 {
		t.Fatalf("large ring %d from %d, small ring %d; want 56 from 5 and 40", large.ring.len(), large.ring.first(), small.ring.len())
	}
	if h.ringBytes != 6000 {
		t.Fatalf("ring bytes = %d, want 6000", h.ringBytes)
	}
	// A ring keeps 32 patches, even over the limit.
	h.opts.ringBytes = 100
	h.trimRings()
	if large.ring.len() != 32 || small.ring.len() != 32 {
		t.Fatalf("rings after a hard limit: %d and %d, want 32 and 32", large.ring.len(), small.ring.len())
	}
	if h.ringBytes != 32*100+32*10 {
		t.Fatalf("ring bytes = %d, want %d", h.ringBytes, 32*100+32*10)
	}
}

func TestLifecycle(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	h := newHub(t, pool)
	if err := h.Ready(t.Context()); err == nil || !strings.Contains(err.Error(), "Try: ") {
		t.Fatalf("Ready before Start = %v, want an error", err)
	}
	if err := h.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := h.Ready(t.Context()); err != nil {
		t.Fatalf("Ready after Start = %v", err)
	}
	var le *Error
	if err := h.Start(t.Context()); !errors.As(err, &le) || !strings.Contains(le.Try, "only once") {
		t.Fatalf("second Start = %v", err)
	}
	h.mu.Lock()
	h.state = stateDraining
	h.mu.Unlock()
	if err := h.Ready(t.Context()); err == nil {
		t.Fatal("Ready while draining is nil")
	}
	if err := h.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(t.Context()); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if err := h.Ready(t.Context()); err == nil {
		t.Fatal("Ready after Close is nil")
	}
	if err := h.Start(t.Context()); !errors.As(err, &le) {
		t.Fatalf("Start after Close = %v", err)
	}
	if _, err := h.acquire(t.Context(), "notes"); !errors.Is(err, errHubClosed) {
		t.Fatalf("acquire after Close = %v", err)
	}
}

func TestCloseWithoutStart(t *testing.T) {
	t.Parallel()
	h := newHub(t, lazyPool(t))
	for range 2 {
		if err := h.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCloseEndsFeeds(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	h := startHub(t, pool)
	f := h.acquireFeed(t, "notes")
	s := f.subscribe(1)
	if err := h.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitWake(t, s, time.Second)
	if _, state := readAll(f, s); state != readClosed {
		t.Fatalf("read after Close = %d, want readClosed", state)
	}
}

func TestStartContextEndsLoops(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	h := newHub(t, pool)
	ctx, cancel := context.WithCancel(t.Context())
	if err := h.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan struct{})
	go func() { h.loops.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the loops did not end with the context of Start")
	}
}

func TestStartWithoutDatabaseKeepsTrying(t *testing.T) {
	t.Parallel()
	h := newHub(t, lazyPool(t), func(o *options) { o.reconnectMin = 10 * time.Millisecond })
	if err := h.Start(t.Context()); err != nil {
		t.Fatalf("Start without a database = %v, want nil", err)
	}
	if err := h.Ready(t.Context()); err != nil {
		t.Fatalf("Ready without a LISTEN connection = %v, want nil", err)
	}
	eventually(t, "a warning for the failed LISTEN", func() bool {
		return strings.Count(h.logs.String(), "cannot open the LISTEN connection") >= 2
	})
}

func TestAcquireConcurrentLoadsOnce(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	publish(t, pool, "notes", Touch("1"))
	h := newHub(t, pool)
	const n = 20
	feeds := make([]*feed, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			f, err := h.acquire(t.Context(), "notes")
			if err != nil {
				t.Error(err)
				return
			}
			feeds[i] = f
		})
	}
	wg.Wait()
	for _, f := range feeds {
		if f != feeds[0] {
			t.Fatal("two loads of one feed")
		}
	}
	if feeds[0].refs != n || feeds[0].head().Seq != 1 {
		t.Fatalf("refs %d, head %v", feeds[0].refs, feeds[0].head())
	}
	for _, f := range feeds {
		h.release(f)
	}
}

func TestAcquireFailureRemovesTheFeed(t *testing.T) {
	t.Parallel()
	h := newHub(t, lazyPool(t), WithBootstrapWait(time.Millisecond))
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if _, err := h.acquire(ctx, "notes"); err == nil {
		t.Fatal("acquire without a database is nil")
	}
	if len(h.feeds) != 0 {
		t.Fatalf("a failed load stays: %v", h.feeds)
	}
	if len(h.boot) != 0 {
		t.Fatal("a failed load keeps its bootstrap slot")
	}
}

// TestNoGoroutineLeak counts goroutines, so it is not parallel.
func TestNoGoroutineLeak(t *testing.T) {
	pool := liveDB(t)
	exec(t, pool, `SELECT 1`)
	settle := func() int {
		runtime.GC()
		time.Sleep(50 * time.Millisecond)
		return runtime.NumGoroutine()
	}
	before := settle()
	h, err := New(pool, WithLogger(discardLogger), WithPollInterval(20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	f, err := h.acquire(t.Context(), "notes")
	if err != nil {
		t.Fatal(err)
	}
	publish(t, pool, "notes", Touch("1"))
	time.Sleep(100 * time.Millisecond)
	h.release(f)
	if err := h.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	after := settle()
	for after > before && time.Now().Before(deadline) {
		after = settle()
	}
	if after > before {
		buf := make([]byte, 1<<20)
		t.Fatalf("goroutines: %d before, %d after Close\n%s", before, after, buf[:runtime.Stack(buf, true)])
	}
}

func TestReadCursor(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	h := newHub(t, pool)
	if c, err := h.readCursor(t.Context(), "notes:3"); err != nil || c != (Cursor{}) {
		t.Fatalf("readCursor of a feed with no row = %v, %v; want 0-0", c, err)
	}
	publish(t, pool, "notes:3", Touch("1"))
	publish(t, pool, "notes:3", Touch("2"))
	row, _ := feedRow(t, pool, "notes:3")
	if c, err := h.readCursor(t.Context(), "notes:3"); err != nil || c != row || c.Seq != 2 {
		t.Fatalf("readCursor = %v, %v; want %v", c, err, row)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var le *Error
	if _, err := h.readCursor(ctx, "notes:3"); !errors.As(err, &le) || le.Try == "" {
		t.Fatalf("readCursor with a canceled context = %v, want an *Error", err)
	}
}
