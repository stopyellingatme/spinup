package live

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/stopyellingatme/spinup/kit/db"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

func TestMigrationUpDownUp(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	m, err := db.NewMigrator(testkit.URL(t, pool), fstest.MapFS{}, db.WithKitMigrations(kitFS(t)), db.WithOutput(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	ctx := t.Context()
	tables := func() int64 {
		return count(t, pool, `SELECT count(*) FROM pg_class WHERE relname IN ('live_feeds', 'live_outbox') AND relkind = 'r'`)
	}
	if got := tables(); got != 2 {
		t.Fatalf("after up: %d tables, want 2", got)
	}
	if err := m.KitDown(ctx); err != nil {
		t.Fatal(err)
	}
	if got := tables(); got != 0 {
		t.Fatalf("after down: %d tables, want 0", got)
	}
	if got := count(t, pool, `SELECT count(*) FROM pg_proc WHERE proname = 'live_outbox_notify'`); got != 0 {
		t.Fatalf("after down: the trigger function exists")
	}
	if err := m.KitUp(ctx); err != nil {
		t.Fatal(err)
	}
	if got := tables(); got != 2 {
		t.Fatalf("after the second up: %d tables, want 2", got)
	}
	publish(t, pool, "notes", Touch("1"))
}

// collect reads notifications until a wait of d times out.
func collect(t *testing.T, conn *pgx.Conn, d time.Duration) []string {
	t.Helper()
	var out []string
	for {
		ctx, cancel := context.WithTimeout(t.Context(), d)
		n, err := conn.WaitForNotification(ctx)
		cancel()
		if err != nil {
			if !pgconn.Timeout(err) {
				t.Fatalf("WaitForNotification: %v", err)
			}
			slices.Sort(out)
			return out
		}
		out = append(out, n.Payload)
	}
}

func TestNotifyOnceForEachFeedAtCommit(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	conn := listenConn(t, pool)
	ctx := t.Context()
	exec(t, pool, `INSERT INTO live_feeds (key, seq) VALUES ('a', 2), ('b', 1), ('c', 1)`)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO live_outbox (feed, seq, changes) VALUES ('a', 1, '[]'), ('a', 2, '[]'), ('b', 1, '[]')`); err != nil {
		t.Fatal(err)
	}
	if got := collect(t, conn, 100*time.Millisecond); len(got) != 0 {
		t.Fatalf("notifications before commit: %q", got)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := collect(t, conn, 300*time.Millisecond), []string{"1 b", "2 a"}; !slices.Equal(got, want) {
		t.Fatalf("notifications after commit = %q, want %q", got, want)
	}

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO live_outbox (feed, seq, changes) VALUES ('c', 1, '[]')`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if got := collect(t, conn, 200*time.Millisecond); len(got) != 0 {
		t.Fatalf("notifications after rollback: %q", got)
	}
}

func TestPublishSequenceHasNoGap(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	const writers = 50
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Go(func() {
			ctx := t.Context()
			tx, err := pool.Begin(ctx)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if err := Publish(ctx, tx, "notes", Upsert(strconv.Itoa(i), map[string]int{"n": i})); err != nil {
				errs <- err
				return
			}
			errs <- tx.Commit(ctx)
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := pool.Query(t.Context(), `SELECT seq FROM live_outbox WHERE feed = 'notes' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	seqs, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range seqs {
		if s != int64(i+1) {
			t.Fatalf("seq %d at position %d: the sequence has a gap: %v", s, i, seqs)
		}
	}
	if len(seqs) != writers {
		t.Fatalf("%d rows, want %d", len(seqs), writers)
	}
	if c, _ := feedRow(t, pool, "notes"); c.Seq != writers {
		t.Fatalf("feed seq = %d, want %d", c.Seq, writers)
	}
}

func TestPublishRollbackLeavesNoRow(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	ctx := t.Context()
	publish(t, pool, "notes:7", Touch("1"))
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := Publish(ctx, tx, "notes:7", Touch("2")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if c, _ := feedRow(t, pool, "notes:7"); c.Seq != 1 {
		t.Fatalf("seq after rollback = %d, want 1", c.Seq)
	}
	if n := count(t, pool, `SELECT count(*) FROM live_outbox WHERE feed = 'notes:7'`); n != 1 {
		t.Fatalf("%d outbox rows after rollback, want 1", n)
	}
}

func TestPublishTwoChangesMakeOnePatch(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	publish(t, pool, "notes", Upsert("7", map[string]any{"id": 7, "body": "x"}), Delete("3"))
	var raw []byte
	if err := pool.QueryRow(t.Context(), `SELECT changes::text FROM live_outbox WHERE feed = 'notes' AND seq = 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0]["op"] != "upsert" || got[0]["id"] != "7" || got[1]["op"] != "delete" || got[1]["id"] != "3" {
		t.Fatalf("changes = %s", raw)
	}
	if _, ok := got[1]["row"]; ok {
		t.Fatalf("a delete has a row: %s", raw)
	}
	if n := count(t, pool, `SELECT count(*) FROM live_outbox`); n != 1 {
		t.Fatalf("%d outbox rows, want 1", n)
	}
}

func TestPublishLimitsWriteNothing(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	big := strings.Repeat("x", 16<<10)
	medium := strings.Repeat("y", 15<<10)
	many := make([]Change, 101)
	for i := range many {
		many[i] = Touch(strconv.Itoa(i))
	}
	cases := []struct {
		name    string
		key     Key
		changes []Change
		try     string
	}{
		{"no change", "notes", nil, "publish between 1 and 100 changes"},
		{"101 changes", "notes", many, "publish between 1 and 100 changes"},
		{"empty ID", "notes", []Change{Touch("")}, "use an ID of 1 to 64"},
		{"long ID", "notes", []Change{Touch(strings.Repeat("a", 65))}, "use an ID of 1 to 64"},
		{"bad ID", "notes", []Change{Delete("a b")}, "use an ID of 1 to 64"},
		{"large row", "notes", []Change{Upsert("1", big)}, "publish live.Touch(id) instead"},
		{"large patch", "notes", []Change{Upsert("1", medium), Upsert("2", medium), Upsert("3", medium), Upsert("4", medium), Upsert("5", medium)}, "publish fewer changes"},
		{"nil row", "notes", []Change{Upsert("1", nil)}, "supply a row"},
		{"bad key", "Notes", []Change{Touch("1")}, "use a lowercase feed name"},
		{"bad scope", "notes:x", []Change{Touch("1")}, "use a lowercase feed name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := t.Context()
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			err = Publish(ctx, tx, c.key, c.changes...)
			var le *Error
			if !errors.As(err, &le) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if !strings.Contains(le.Try, c.try) || !strings.Contains(err.Error(), "\nTry: ") {
				t.Fatalf("err = %q, want a Try line with %q", err, c.try)
			}
			// The transaction is still good: Publish ran no SQL.
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
	if n := count(t, pool, `SELECT count(*) FROM live_feeds`) + count(t, pool, `SELECT count(*) FROM live_outbox`); n != 0 {
		t.Fatalf("the limits wrote %d rows", n)
	}
}

func TestPublishFirstCreatesFeedAndKeepsEpoch(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	publish(t, pool, "notes:42", Touch("1"))
	first, ok := feedRow(t, pool, "notes:42")
	if !ok || first.Seq != 1 {
		t.Fatalf("feed after the first publish = %v, %v; want seq 1", first, ok)
	}
	if first.Epoch <= 1_700_000_000_000 {
		t.Fatalf("epoch = %d, want a time in milliseconds after 1.7e12", first.Epoch)
	}
	publish(t, pool, "notes:42", Touch("2"))
	publish(t, pool, "notes:42", Touch("3"))
	later, _ := feedRow(t, pool, "notes:42")
	if later.Epoch != first.Epoch || later.Seq != 3 {
		t.Fatalf("feed after three publishes = %v, want epoch %d and seq 3", later, first.Epoch)
	}
}

func TestPublishClosedOrNilTransaction(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	err = Publish(ctx, tx, "notes", Touch("1"))
	var le *Error
	if !errors.As(err, &le) || !errors.Is(err, pgx.ErrTxClosed) || !strings.Contains(le.Try, "before the transaction commits") {
		t.Fatalf("closed tx: err = %v", err)
	}
	err = Publish(ctx, nil, "notes", Touch("1"))
	if !errors.As(err, &le) || !strings.Contains(err.Error(), "the transaction is nil") || le.Try == "" {
		t.Fatalf("nil tx: err = %v", err)
	}
}

func TestPublishWithoutMigration(t *testing.T) {
	t.Parallel()
	skipNoDB(t)
	pool := testkit.DB(t)
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	err = Publish(ctx, tx, "notes", Touch("1"))
	var le *Error
	if !errors.As(err, &le) || le.Try != migrateTry {
		t.Fatalf("err = %v, want the migrate Try line", err)
	}
	_, err = Prune(ctx, pool, PruneOptions{})
	if !errors.As(err, &le) || le.Try != migrateTry {
		t.Fatalf("Prune err = %v, want the migrate Try line", err)
	}
}

// TestPublishSpan changes the global tracer provider, so it is not parallel.
func TestPublishSpan(t *testing.T) {
	pool := liveDB(t)
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = tp.Shutdown(context.Background()) })
	publish(t, pool, "notes:5", Touch("1"))
	publish(t, pool, "notes:5", Touch("2"))
	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("%d spans, want 2", len(spans))
	}
	s := spans[1]
	want := map[attribute.Key]attribute.Value{"live.feed": attribute.StringValue("notes"), "live.seq": attribute.Int64Value(2)}
	got := map[attribute.Key]attribute.Value{}
	for _, a := range s.Attributes {
		got[a.Key] = a.Value
	}
	if s.Name != "live.publish" || got["live.feed"] != want["live.feed"] || got["live.seq"] != want["live.seq"] {
		t.Fatalf("span %s with %v, want live.publish with %v", s.Name, got, want)
	}
}

func TestPrune(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	for range 3 {
		publish(t, pool, "notes", Touch("1"))
	}
	publish(t, pool, "idle", Touch("1"))
	publish(t, pool, "recent", Touch("1"))
	exec(t, pool, `UPDATE live_outbox SET created_at = now() - interval '2 hours' WHERE feed = 'notes' AND seq < 3`)
	exec(t, pool, `UPDATE live_outbox SET created_at = now() - interval '2 hours' WHERE feed IN ('idle', 'recent')`)
	exec(t, pool, `UPDATE live_feeds SET updated_at = now() - interval '40 days' WHERE key = 'idle'`)

	got, err := Prune(t.Context(), pool, PruneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Pruned{Patches: 4, Feeds: 1}); got != want {
		t.Fatalf("Prune = %+v, want %+v", got, want)
	}
	rows, err := pool.Query(t.Context(), `SELECT feed || ':' || seq FROM live_outbox ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	left, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(left, []string{"notes:3"}) {
		t.Fatalf("outbox after Prune = %v, want [notes:3]", left)
	}
	if _, ok := feedRow(t, pool, "idle"); ok {
		t.Fatal("the idle feed with no patch stays")
	}
	if _, ok := feedRow(t, pool, "recent"); !ok {
		t.Fatal("the feed with a recent update went")
	}
	if got, err := Prune(t.Context(), pool, PruneOptions{}); err != nil || got != (Pruned{}) {
		t.Fatalf("second Prune = %+v, %v; want nothing", got, err)
	}
}

func TestPruneWithConcurrentPublish(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	pruneErr := make(chan error, 1)
	wg.Go(func() {
		for ctx.Err() == nil {
			if _, err := Prune(ctx, pool, PruneOptions{Patches: time.Microsecond, Feeds: time.Microsecond}); err != nil && ctx.Err() == nil {
				pruneErr <- err
				return
			}
		}
	})
	for i := range 200 {
		publish(t, pool, Key("notes:"+strconv.Itoa(i%4)), Touch(strconv.Itoa(i)))
	}
	cancel()
	wg.Wait()
	select {
	case err := <-pruneErr:
		t.Fatalf("Prune failed during the publishes: %v", err)
	default:
	}
}
