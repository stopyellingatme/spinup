package apikeys

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The hook blocks before connection attempts, so this regression needs no socket.
func TestQuotaDatabaseWaitWithoutSocket(t *testing.T) {
	for _, operation := range []string{"flush", "day"} {
		t.Run(operation, func(t *testing.T) {
			cfg, err := pgxpool.ParseConfig("postgres://postgres:postgres@127.0.0.1:55433/postgres?sslmode=disable")
			if err != nil {
				t.Fatal(err)
			}
			blocked, release := make(chan struct{}), make(chan struct{})
			var entered, released sync.Once
			unblock := func() { released.Do(func() { close(release) }) }
			cfg.BeforeConnect = func(ctx context.Context, _ *pgx.ConnConfig) error {
				entered.Do(func() { close(blocked) })
				select {
				case <-release:
					return errors.New("database unavailable")
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			t.Cleanup(unblock)
			now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
			s := dbStore(t, pool, testConfig(), WithClock(func() time.Time { return now }))
			req := testRequest()
			who := userDay{req.Key.UserID, utcDay(now)}
			pendingKey := keyDay{req.Key.ID, req.Key.UserID, who.day}
			s.days[who] = &dayUsage{base: 10, local: 1}
			s.pending[pendingKey] = 1
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				if operation == "flush" {
					_ = s.flush(ctx)
				} else {
					cold := req
					cold.Key.ID++
					cold.Key.UserID++
					quotaCall(s, ctx, cold)
				}
			}()
			defer func() {
				unblock()
				<-done
			}()
			select {
			case <-blocked:
			case <-ctx.Done():
				t.Fatal("database work did not reach the connection hook")
			}
			assertQuotaCallUnblocked(t, s, req)
			if used := s.cachedUsed(who); used != 12 {
				t.Fatalf("usage during database work = %d, want 12", used)
			}
			unblock()
			<-done
			if operation == "flush" {
				if s.Ready(t.Context()) == nil {
					t.Fatal("failed flush did not affect readiness")
				}
				if err := s.flush(t.Context()); err == nil {
					t.Fatal("retry with unavailable database succeeded")
				}
			}
			if s.pending[pendingKey] != 2 || s.cachedUsed(who) != 12 {
				t.Fatal("database failure lost requests that arrived during the wait")
			}
		})
	}
}

func waitQuotaQuery(t *testing.T, pool *pgxpool.Pool, query string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1)`, query+"%").Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("quota query did not wait for the database lock: %s", query)
		case <-poll.C:
		}
	}
}

func quotaCall(s *Store, ctx context.Context, req Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(requestContext(ctx, req), http.MethodGet, "/api/notes", nil)
	s.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(w, r)
	return w
}

func assertQuotaCallUnblocked(t *testing.T, s *Store, req Request) {
	t.Helper()
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() { answered <- quotaCall(s, t.Context(), req) }()
	select {
	case w := <-answered:
		if w.Code != http.StatusNoContent {
			t.Fatalf("cached request: %d %s", w.Code, w.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("cached request waited for database work")
	}
}

func TestStoreFlushDoesNotBlockRequests(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			pool := keyDB(t, false)
			id := keyUser(t, pool, "flush@example.com")
			now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
			s := dbStore(t, pool, testConfig(), WithClock(func() time.Time { return now }))
			k, _, err := s.Create(t.Context(), id, CreateInput{Name: "flush", Scope: ScopeRead})
			if err != nil {
				t.Fatal(err)
			}
			req := Request{Key: k, PlanName: "free", Plan: s.cfg.Plans["free"]}
			if w := quotaCall(s, t.Context(), req); w.Code != http.StatusNoContent {
				t.Fatal(w.Body)
			}
			if err := s.flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			if w := quotaCall(s, t.Context(), req); w.Code != http.StatusNoContent {
				t.Fatal(w.Body)
			}
			tx, err := pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err := tx.Exec(t.Context(), `SELECT 1 FROM api_key_usage WHERE key_id=$1 FOR UPDATE`, k.ID); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			done := make(chan struct{})
			var flushErr error
			go func() {
				defer close(done)
				flushErr = s.flush(ctx)
			}()
			defer func() {
				cancel()
				_ = tx.Rollback(context.Background())
				<-done
			}()
			waitQuotaQuery(t, pool, "INSERT INTO api_key_usage")
			assertQuotaCallUnblocked(t, s, req)
			who := userDay{id, utcDay(now)}
			if used := s.cachedUsed(who); used != 3 {
				t.Fatalf("usage during flush = %d, want 3", used)
			}
			if fail {
				cancel()
			} else if err := tx.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			<-done
			if (flushErr != nil) != fail {
				t.Fatalf("flush error = %v, want failure %v", flushErr, fail)
			}
			s.quotaMu.Lock()
			d := *s.days[who]
			pending := s.pending[keyDay{k.ID, id, who.day}]
			s.quotaMu.Unlock()
			wantBase, wantLocal := int64(2), int64(1)
			if fail {
				wantBase, wantLocal = 1, 2
			}
			if d.base != wantBase || d.local != wantLocal || pending != wantLocal {
				t.Fatalf("after flush: base=%d local=%d pending=%d", d.base, d.local, pending)
			}
			_ = tx.Rollback(t.Context())
			if err := s.flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			usage, err := s.Usage(t.Context(), id, 1)
			if err != nil || len(usage) != 1 || usage[0].Count != 3 || s.cachedUsed(who) != 3 {
				t.Fatalf("retried usage = %v, error = %v", usage, err)
			}
		})
	}
}

func TestStoreDayReadDoesNotBlockCachedUser(t *testing.T) {
	pool := keyDB(t, false)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	s := dbStore(t, pool, testConfig(), WithClock(func() time.Time { return now }))
	var reqs []Request
	for _, email := range []string{"cached@example.com", "cold@example.com"} {
		id := keyUser(t, pool, email)
		k, _, err := s.Create(t.Context(), id, CreateInput{Name: "day", Scope: ScopeRead})
		if err != nil {
			t.Fatal(err)
		}
		reqs = append(reqs, Request{Key: k, PlanName: "free", Plan: s.cfg.Plans["free"]})
	}
	if w := quotaCall(s, t.Context(), reqs[0]); w.Code != http.StatusNoContent {
		t.Fatal(w.Body)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(t.Context(), `LOCK TABLE api_key_usage IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() { answered <- quotaCall(s, ctx, reqs[1]) }()
	waitQuotaQuery(t, pool, "SELECT COALESCE(sum(g.count)")
	assertQuotaCallUnblocked(t, s, reqs[0])
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if w := <-answered; w.Code != http.StatusNoContent {
		t.Fatalf("cold request: %d %s", w.Code, w.Body)
	}
}
