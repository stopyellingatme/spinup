package live

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stopyellingatme/spinup/kit/testkit"
)

// migrationPath is the kit migration of the live pack (main loop only).
const migrationPath = "../../../internal/templates/packs/live/files/api/db/kit/00011_live.sql"

// kitFS returns the kit migration space with the live migration only. The
// live tables have no foreign key to users, so no auth migration is needed.
func kitFS(t *testing.T) fstest.MapFS {
	t.Helper()
	data, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatal(err)
	}
	return fstest.MapFS{"00011_live.sql": &fstest.MapFile{Data: data, Mode: 0o644}}
}

// skipNoDB skips the test when DATABASE_URL is empty.
func skipNoDB(t *testing.T) {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL is empty")
	}
}

// liveDB returns a pool on a new database that holds the live migration.
func liveDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	skipNoDB(t)
	return testkit.DBWithMigrations(t, fstest.MapFS{}, testkit.WithKitMigrations(kitFS(t)))
}

// publish runs Publish in a new transaction and commits it.
func publish(t *testing.T, pool *pgxpool.Pool, key Key, changes ...Change) {
	t.Helper()
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := Publish(ctx, tx, key, changes...); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// feedRow returns the epoch and seq of a feed row, and false when it has no row.
func feedRow(t *testing.T, pool *pgxpool.Pool, key Key) (Cursor, bool) {
	t.Helper()
	var c Cursor
	err := pool.QueryRow(t.Context(), `SELECT epoch, seq FROM live_feeds WHERE key = $1`, string(key)).Scan(&c.Epoch, &c.Seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return Cursor{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return c, true
}

func count(t *testing.T, pool *pgxpool.Pool, query string) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(t.Context(), query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// syncBuffer is a log destination that is safe for concurrent writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// testHub is a hub with a manual metric reader and a log buffer.
type testHub struct {
	*Hub
	reader *sdkmetric.ManualReader
	logs   *syncBuffer
}

// newHub creates a hub. It does not start it. A cleanup closes it.
func newHub(t *testing.T, pool *pgxpool.Pool, opts ...Option) *testHub {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	logs := &syncBuffer{}
	base := []Option{WithMeterProvider(mp), WithLogger(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))}
	h, err := New(pool, append(base, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := h.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return &testHub{Hub: h, reader: reader, logs: logs}
}

// startHub creates and starts a hub.
func startHub(t *testing.T, pool *pgxpool.Pool, opts ...Option) *testHub {
	t.Helper()
	h := newHub(t, pool, opts...)
	if err := h.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return h
}

// counter returns the sum of the data points of an Int64 counter.
func (h *testHub) counter(t *testing.T, name string) int64 {
	t.Helper()
	var data metricdata.ResourceMetrics
	if err := h.reader.Collect(context.Background(), &data); err != nil {
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
					total += p.Value
				}
			case metricdata.Gauge[int64]:
				for _, p := range d.DataPoints {
					total += p.Value
				}
			}
		}
	}
	return total
}

// acquire acquires a feed and releases it at the end of the test.
func (h *testHub) acquireFeed(t *testing.T, key Key) *feed {
	t.Helper()
	f, err := h.acquire(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.release(f) })
	return f
}

// settle waits until the dirty set is empty and no fetch runs.
func (h *testHub) settle(t *testing.T) {
	t.Helper()
	eventually(t, "the dirty set is empty", func() bool {
		h.dmu.Lock()
		defer h.dmu.Unlock()
		return len(h.pending) == 0 && len(h.fetchState) == 0
	})
}

// eventually fails the test when cond is not true within 5 seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	const d = 5 * time.Second
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s: %s", d, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitWake waits for a wake of the sub.
func waitWake(t *testing.T, s *sub, d time.Duration) {
	t.Helper()
	select {
	case <-s.wake:
	case <-time.After(d):
		t.Fatalf("the sub had no wake in %s", d)
	}
}

// readAll reads the frames of the sub.
func readAll(f *feed, s *sub) ([][]byte, readState) {
	dst := make([][]byte, 4096)
	n, state := f.read(s, dst)
	return dst[:n], state
}

// listenConn opens a connection that listens on the live channel.
func listenConn(t *testing.T, pool *pgxpool.Pool) *pgx.Conn {
	t.Helper()
	conn, err := pgx.ConnectConfig(t.Context(), pool.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err := conn.Exec(t.Context(), "LISTEN "+notifyChannel); err != nil {
		t.Fatal(err)
	}
	return conn
}

// discardLogger is a logger for hubs whose logs a test does not read.
var discardLogger = slog.New(slog.NewTextHandler(io.Discard, nil))
