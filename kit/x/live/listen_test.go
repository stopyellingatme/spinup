package live

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestListenSurvivesWaitTimeout verifies claim a of ADR 0033 appendix A: after
// WaitForNotification returns a context timeout, the same connection still
// receives a later notification.
func TestListenSurvivesWaitTimeout(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	conn := listenConn(t, pool)
	for range 3 {
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		_, err := conn.WaitForNotification(ctx)
		cancel()
		if !pgconn.Timeout(err) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("WaitForNotification = %v, want a timeout", err)
		}
		if conn.IsClosed() {
			t.Fatal("the connection closed after a timeout")
		}
	}
	exec(t, pool, `SELECT pg_notify('spinup_live', '7 notes')`)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	n, err := conn.WaitForNotification(ctx)
	if err != nil {
		t.Fatalf("WaitForNotification after a timeout: %v", err)
	}
	if n.Channel != notifyChannel || n.Payload != "7 notes" {
		t.Fatalf("notification = %s %q", n.Channel, n.Payload)
	}
	if err := conn.Ping(t.Context()); err != nil {
		t.Fatalf("Ping after the notification: %v", err)
	}
}

func TestParsePayload(t *testing.T) {
	t.Parallel()
	for payload, want := range map[string]Key{"7 notes": "notes", "1 notes:42": "notes:42"} {
		if key, _, ok := parsePayload(payload); !ok || key != want {
			t.Errorf("parsePayload(%q) = %q, %v", payload, key, ok)
		}
	}
	for _, payload := range []string{"", "7", "notes", "x notes", "-1 notes", "7 Notes", "7  notes", "1234567890123456789 notes", "7 notes:x"} {
		if _, _, ok := parsePayload(payload); ok {
			t.Errorf("parsePayload(%q) is ok", payload)
		}
	}
}

func TestListenerDeliversPatch(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	h := startHub(t, pool, WithPollInterval(time.Hour))
	if !h.listenerUp.Load() {
		t.Fatal("the listener is not up after Start")
	}
	if got := h.counter(t, "live.listener.up"); got != 1 {
		t.Fatalf("live.listener.up = %d, want 1", got)
	}
	f := h.acquireFeed(t, "notes:42")
	if c := f.head(); c != (Cursor{}) {
		t.Fatalf("head of a feed with no row = %v, want 0-0", c)
	}
	s := f.subscribe(1)
	defer f.unsubscribe(s)
	publish(t, pool, "notes:42", Upsert("7", map[string]any{"id": 7}))
	waitWake(t, s, 5*time.Second)
	frames, state := readAll(f, s)
	if state != readOK || len(frames) != 1 {
		t.Fatalf("read = %d frames, state %d; want 1 frame and readOK", len(frames), state)
	}
	row, _ := feedRow(t, pool, "notes:42")
	if f.head() != row {
		t.Fatalf("head = %v, want %v", f.head(), row)
	}
	want := patchFrame(row.Epoch, 1, []byte(`[{"id": "7", "op": "upsert", "row": {"id": 7}}]`))
	if !bytes.Equal(frames[0], want) {
		t.Fatalf("frame = %q, want %q", frames[0], want)
	}
	if got := h.counter(t, "live.patch.received"); got != 1 {
		t.Fatalf("live.patch.received = %d, want 1", got)
	}
	if got := h.counter(t, "live.poll.recovered"); got != 0 {
		t.Fatalf("live.poll.recovered = %d, want 0", got)
	}
}

func TestListenerReconnectAndCatchUp(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	// The gate holds the reconnect until the patch is committed, so only the
	// catch-up poll can find it.
	gate := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(gate) }) }
	h := startHub(t, pool, WithPollInterval(time.Hour), func(o *options) {
		o.reconnectMin = 20 * time.Millisecond
		o.beforeDial = func() { <-gate }
	})
	t.Cleanup(open) // runs before the Close of the hub
	f := h.acquireFeed(t, "notes")
	s := f.subscribe(1)
	defer f.unsubscribe(s)
	h.settle(t)
	pid := h.listenPID.Load()
	exec(t, pool, `SELECT pg_terminate_backend($1)`, int32(pid)) //nolint:gosec // a PostgreSQL PID fits in int32
	eventually(t, "the listener is down", func() bool { return !h.listenerUp.Load() })
	publish(t, pool, "notes", Touch("1"))
	open()
	waitWake(t, s, 10*time.Second)
	if frames, state := readAll(f, s); state != readOK || len(frames) != 1 {
		t.Fatalf("read = %d frames, state %d", len(frames), state)
	}
	eventually(t, "the listener is up again", h.listenerUp.Load)
	if h.listenPID.Load() == pid {
		t.Fatal("the listener PID did not change")
	}
	if got := h.counter(t, "live.poll.recovered"); got != 1 {
		t.Fatalf("live.poll.recovered = %d, want 1; logs:\n%s", got, h.logs)
	}
	// The new connection receives notifications.
	publish(t, pool, "notes", Touch("2"))
	waitWake(t, s, 5*time.Second)
	if frames, state := readAll(f, s); state != readOK || len(frames) != 1 {
		t.Fatalf("read after reconnect = %d frames, state %d", len(frames), state)
	}
	if !strings.Contains(h.logs.String(), "the LISTEN connection failed") {
		t.Fatalf("no warning for the lost connection in the logs:\n%s", h.logs)
	}
}

func TestListenerIgnoresBadPayload(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	h := startHub(t, pool, WithPollInterval(time.Hour))
	f := h.acquireFeed(t, "notes")
	s := f.subscribe(1)
	defer f.unsubscribe(s)
	exec(t, pool, `SELECT pg_notify('spinup_live', 'garbage')`)
	exec(t, pool, `SELECT pg_notify('spinup_live', '1 NOTES')`)
	publish(t, pool, "notes", Touch("1"))
	waitWake(t, s, 5*time.Second)
	if frames, state := readAll(f, s); state != readOK || len(frames) != 1 {
		t.Fatalf("read = %d frames, state %d", len(frames), state)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "level=DEBUG") || !strings.Contains(logs, "bad payload") || !strings.Contains(logs, "garbage") {
		t.Fatalf("no debug line for the bad payload:\n%s", logs)
	}
	if !h.listenerUp.Load() {
		t.Fatal("a bad payload stopped the listener")
	}
}

func TestPollOnly(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	h := startHub(t, pool, WithPollInterval(50*time.Millisecond), func(o *options) { o.noListen = true })
	f := h.acquireFeed(t, "notes:1")
	s := f.subscribe(1)
	defer f.unsubscribe(s)
	// The fetch after the load must end first, so only the poll finds the patch.
	h.settle(t)
	publish(t, pool, "notes:1", Touch("1"))
	waitWake(t, s, 5*time.Second)
	if frames, state := readAll(f, s); state != readOK || len(frames) != 1 {
		t.Fatalf("read = %d frames, state %d", len(frames), state)
	}
	if h.listenerUp.Load() {
		t.Fatal("the listener is up with no LISTEN connection")
	}
	if got := h.counter(t, "live.listener.up"); got != 0 {
		t.Fatalf("live.listener.up = %d, want 0", got)
	}
	if got := h.counter(t, "live.poll.recovered"); got != 1 {
		t.Fatalf("live.poll.recovered = %d, want 1", got)
	}
}
