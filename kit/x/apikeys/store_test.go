package apikeys

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/audit"
	"github.com/stopyellingatme/spinup/kit/auth"
)

func TestStoreFailurePreservesSentinels(t *testing.T) {
	for _, sentinel := range []error{ErrUnknownPlan, ErrKeyNotFound, ErrUserNotFound, ErrTooManyKeys, ErrInvalidKey} {
		for _, err := range []error{sentinel, fmt.Errorf("transaction: %w", sentinel)} {
			got := storeFailure("change the key", err)
			if got.Error() != err.Error() || !errors.Is(got, sentinel) || strings.Count(got.Error(), "Try:") != 1 {
				t.Errorf("sentinel changed: %v", got)
			}
		}
	}
	dbErr := errors.New("database unavailable")
	err := storeFailure("change the key", dbErr)
	if !errors.Is(err, dbErr) || strings.Count(err.Error(), "Try:") != 1 || !strings.Contains(err.Error(), "check the database connection") {
		t.Fatalf("database error = %v", err)
	}
}

func TestStoreSentinelMessages(t *testing.T) {
	pool := keyDB(t, false)
	id := keyUser(t, pool, "sentinel@example.com")
	cfg := testConfig()
	cfg.MaxKeys = 1
	s := dbStore(t, pool, cfg)
	if _, _, err := s.Create(t.Context(), id, CreateInput{Name: "first", Scope: ScopeRead}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		want error
		call func() error
	}{
		{"invalid key", ErrInvalidKey, func() error {
			_, _, err := s.Create(t.Context(), id, CreateInput{})
			return err
		}},
		{"too many keys", ErrTooManyKeys, func() error {
			_, _, err := s.Create(t.Context(), id, CreateInput{Name: "second", Scope: ScopeRead})
			return err
		}},
		{"create missing user", ErrUserNotFound, func() error {
			_, _, err := s.Create(t.Context(), -1, CreateInput{Name: "missing", Scope: ScopeRead})
			return err
		}},
		{"missing key", ErrKeyNotFound, func() error { return s.Revoke(t.Context(), id, -1) }},
		{"unknown plan", ErrUnknownPlan, func() error { return s.SetPlan(t.Context(), "sentinel@example.com", "missing") }},
		{"plan missing user", ErrUserNotFound, func() error { return s.SetPlan(t.Context(), "missing@example.com", "free") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if strings.Count(err.Error(), "Try:") != 1 || strings.Contains(err.Error(), "check the database connection") {
				t.Fatalf("sentinel has incorrect advice: %v", err)
			}
		})
	}
}

func TestNewStoreConfig(t *testing.T) {
	if _, err := NewStore(nil, Config{}); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatal(err)
	}
	if _, err := NewStore(nil, testConfig()); err == nil {
		t.Fatal("nil pool accepted")
	}
	now := time.Now()
	s := testStore(t, &now)
	cfg := testConfig()
	other := dbStore(t, s.pool, cfg)
	cfg.Plans["free"] = Plan{PerMinute: 1, PerDay: 1}
	if other.cfg.Plans["free"].PerDay != 1000 {
		t.Fatal("configuration is mutable")
	}
}

func TestStoreKeys(t *testing.T) {
	pool := keyDB(t, false)
	id := keyUser(t, pool, "owner@example.com")
	other := keyUser(t, pool, "other@example.com")
	now := time.Now().UTC().Truncate(time.Microsecond)
	cfg := testConfig()
	cfg.MaxKeys = 2
	s := dbStore(t, pool, cfg, WithClock(func() time.Time { return now }))
	ctx := t.Context()
	k, token, err := s.Create(ctx, id, CreateInput{Name: "automation", Scope: ScopeRead, ExpiresIn: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if k.ID == 0 || k.UserID != id || k.Display != token[:10] || k.ExpiresAt == nil || !k.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("key=%+v", k)
	}
	marked := context.WithValue(ctx, requestKey{}, "old marker")
	gotCtx, u, err := s.AuthenticateBearer(marked, token)
	req, ok := KeyFrom(gotCtx)
	if err != nil || !ok || u.ID != id || u.Role != "manager" || req.Key.ID != k.ID || req.PlanName != "free" {
		t.Fatalf("auth: %+v %+v %v", u, req, err)
	}
	second, _, err := s.Create(ctx, id, CreateInput{Name: "second", Scope: ScopeWrite})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Create(ctx, id, CreateInput{Name: "third", Scope: ScopeRead}); !errors.Is(err, ErrTooManyKeys) {
		t.Fatal(err)
	}
	keys, err := s.List(ctx, id)
	if err != nil || len(keys) != 2 || keys[0].ID != k.ID {
		t.Fatalf("list=%v %v", keys, err)
	}
	if err := s.Revoke(ctx, other, k.ID); !errors.Is(err, ErrKeyNotFound) {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, 0, k.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AuthenticateBearer(ctx, token); !errors.Is(err, auth.ErrBearerInvalid) {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, id, k.ID); !errors.Is(err, ErrKeyNotFound) {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, id, 999999); !errors.Is(err, ErrKeyNotFound) {
		t.Fatal(err)
	}
	keys, err = s.List(ctx, id)
	if err != nil || len(keys) != 1 || keys[0].ID != second.ID {
		t.Fatalf("list=%v %v", keys, err)
	}
	expiring, expiryToken, err := s.Create(ctx, id, CreateInput{Name: "expiry", Scope: ScopeRead, ExpiresIn: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	if _, _, err := s.AuthenticateBearer(ctx, expiryToken); !errors.Is(err, auth.ErrBearerInvalid) {
		t.Fatalf("expired %d: %v", expiring.ID, err)
	}
	if _, _, err := s.AuthenticateBearer(ctx, "key_"+strings.Repeat("x", 43)); !errors.Is(err, auth.ErrBearerInvalid) {
		t.Fatal(err)
	}
	if _, _, err := s.Create(ctx, 999999, CreateInput{Name: "missing", Scope: ScopeRead}); !errors.Is(err, ErrUserNotFound) {
		t.Fatal(err)
	}
}

func TestStoreConcurrentMaxKeys(t *testing.T) {
	pool := keyDB(t, false)
	id := keyUser(t, pool, "max@example.com")
	cfg := testConfig()
	cfg.MaxKeys = 1
	s := dbStore(t, pool, cfg)
	var wg sync.WaitGroup
	results := make(chan error, 10)
	for range 10 {
		wg.Go(func() {
			_, _, err := s.Create(t.Context(), id, CreateInput{Name: "concurrent", Scope: ScopeRead})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	created := 0
	for err := range results {
		if err == nil {
			created++
		} else if !errors.Is(err, ErrTooManyKeys) {
			t.Fatal(err)
		}
	}
	if created != 1 {
		t.Fatalf("created=%d", created)
	}
}

func TestStorePlansAndAccounts(t *testing.T) {
	pool := keyDB(t, false)
	id := keyUser(t, pool, "plan@example.com")
	cfg := testConfig()
	cfg.Plans["pro"] = Plan{PerMinute: 600, PerDay: 100000}
	var logs bytes.Buffer
	s := dbStore(t, pool, cfg, WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	ctx := t.Context()
	if name, _, err := s.PlanOf(ctx, id); err != nil || name != "free" {
		t.Fatalf("%s %v", name, err)
	}
	if err := s.SetPlan(ctx, " PLAN@example.com ", "pro"); err != nil {
		t.Fatal(err)
	}
	if name, p, err := s.PlanOf(ctx, id); err != nil || name != "pro" || p.PerDay != 100000 {
		t.Fatalf("%s %+v %v", name, p, err)
	}
	k, token, err := s.Create(ctx, id, CreateInput{Name: "plan", Scope: ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := s.AuthenticateBearer(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := KeyFrom(c)
	if r.PlanName != "pro" {
		t.Fatal(r)
	}
	accounts, err := s.Accounts(ctx)
	if err != nil || len(accounts) != 1 || accounts[0].Plan != "pro" || len(accounts[0].Keys) != 1 || accounts[0].Keys[0].ID != k.ID {
		t.Fatalf("accounts=%+v %v", accounts, err)
	}
	if err := s.SetPlan(ctx, "missing@example.com", "pro"); !errors.Is(err, ErrUserNotFound) {
		t.Fatal(err)
	}
	if err := s.SetPlan(ctx, "plan@example.com", "missing"); !errors.Is(err, ErrUnknownPlan) {
		t.Fatal(err)
	}
	if _, _, err := s.PlanOf(ctx, 999999); !errors.Is(err, ErrUserNotFound) {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE api_key_plans SET plan='retired' WHERE user_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		c, _, err = s.AuthenticateBearer(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		r, _ = KeyFrom(c)
		if r.PlanName != "free" {
			t.Fatal(r)
		}
		if name, _, err := s.PlanOf(ctx, id); err != nil || name != "free" {
			t.Fatalf("%s %v", name, err)
		}
	}
	if strings.Count(logs.String(), "retired") != 1 {
		t.Fatalf("warnings=%s", logs.String())
	}
}

func serveKey(t *testing.T, s *Store, token string) int {
	t.Helper()
	ctx, _, err := s.AuthenticateBearer(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(t.Context(), "GET", "/api/notes", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	s.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(w, r)
	return w.Code
}

func TestStoreUsageAndSharedDay(t *testing.T) {
	pool := keyDB(t, false)
	id := keyUser(t, pool, "quota@example.com")
	cfg := testConfig()
	cfg.Plans["free"] = Plan{PerMinute: 2, PerDay: 3}
	now := time.Now().UTC().Truncate(time.Microsecond)
	s := dbStore(t, pool, cfg, WithClock(func() time.Time { return now }))
	other := dbStore(t, pool, cfg, WithClock(func() time.Time { return now }))
	k, token, err := s.Create(t.Context(), id, CreateInput{Name: "quota", Scope: ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	// Both stores initialize their baseline before either flushes.
	if serveKey(t, s, token) != 204 || serveKey(t, other, token) != 204 {
		t.Fatal("initial request failed")
	}
	if err := s.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := other.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if serveKey(t, s, token) != 204 {
		t.Fatal("third request failed")
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := other.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if serveKey(t, other, token) != 429 {
		t.Fatal("replica did not enforce shared day budget")
	}
	usage, err := s.Usage(t.Context(), id, 30)
	if err != nil || len(usage) != 1 || usage[0].Count != 3 || usage[0].KeyID != k.ID {
		t.Fatalf("usage=%v %v", usage, err)
	}
	keys, err := s.List(t.Context(), id)
	if err != nil || keys[0].LastUsedAt == nil {
		t.Fatalf("last used missing: %v", err)
	}
	accounts, err := s.Accounts(t.Context())
	if err != nil || accounts[0].Today != 3 {
		t.Fatalf("accounts=%v %v", accounts, err)
	}
}

func TestStoreMidnightAndFlushFailure(t *testing.T) {
	pool := keyDB(t, false)
	id := keyUser(t, pool, "midnight@example.com")
	now := utcDay(time.Now()).Add(24*time.Hour - time.Second)
	s := dbStore(t, pool, testConfig(), WithClock(func() time.Time { return now }))
	_, token, err := s.Create(t.Context(), id, CreateInput{Name: "midnight", Scope: ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	if serveKey(t, s, token) != 204 {
		t.Fatal("old day failed")
	}
	oldDay := utcDay(now).Format("2006-01-02")
	now = now.Add(2 * time.Second)
	if serveKey(t, s, token) != 204 {
		t.Fatal("new day failed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.flush(ctx); err == nil {
		t.Fatal("canceled flush succeeded")
	}
	if s.Ready(t.Context()) == nil || len(s.pending) != 2 {
		t.Fatal("failed flush lost counts or readiness error")
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
	usage, err := s.Usage(t.Context(), id, 30)
	if err != nil || len(usage) != 2 || usage[0].Day != oldDay || usage[0].Count != 1 || usage[1].Count != 1 {
		t.Fatalf("usage=%v %v", usage, err)
	}
}

func TestStoreStartAndClose(t *testing.T) {
	pool := keyDB(t, false)
	id := keyUser(t, pool, "close@example.com")
	s := dbStore(t, pool, testConfig())
	if s.Ready(t.Context()) == nil {
		t.Fatal("ready before first flush")
	}
	if err := s.Start(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context(), time.Second); err == nil {
		t.Fatal("second Start succeeded")
	}
	_, token, err := s.Create(t.Context(), id, CreateInput{Name: "close", Scope: ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	if serveKey(t, s, token) != 204 {
		t.Fatal("request failed")
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	usage, err := s.Usage(t.Context(), id, 1)
	if err != nil || len(usage) != 1 || usage[0].Count != 1 {
		t.Fatalf("usage=%v %v", usage, err)
	}
}

func TestStorePruneAndAudit(t *testing.T) {
	pool := keyDB(t, true)
	id := keyUser(t, pool, "audit@example.com")
	now := time.Now().UTC().Truncate(time.Microsecond)
	s := dbStore(t, pool, testConfig(), WithClock(func() time.Time { return now }))
	ctx := audit.WithActor(t.Context(), audit.Actor{Kind: audit.KindUser, ID: id, Email: "audit@example.com", Source: audit.SourceAPI, Reason: "test"})
	k, _, err := s.Create(ctx, id, CreateInput{Name: "audit", Scope: ScopeWrite})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPlan(ctx, "audit@example.com", "free"); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, id, k.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE table_name IN ('api_keys','api_key_plans') AND reason='test'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("audit events=%d", count)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO api_key_usage(key_id,day,count) VALUES($1,$2,1)`, k.ID, utcDay(now)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(48 * time.Hour)
	deleted, err := s.Prune(ctx, 24*time.Hour)
	if err != nil || deleted != 2 {
		t.Fatalf("pruned=%d %v", deleted, err)
	}
}

func TestStoreStartDatabaseFailure(t *testing.T) {
	now := time.Now()
	s := testStore(t, &now)
	s.pool.Close()
	if err := s.Start(t.Context(), 0); err != nil {
		t.Fatalf("database failure escaped Start: %v", err)
	}
	if err := s.Ready(t.Context()); err == nil {
		t.Fatal("database failure did not affect Ready")
	}
	if err := s.Close(t.Context()); err == nil {
		t.Fatal("final flush failure was lost")
	}
	if err := s.Close(t.Context()); err == nil {
		t.Fatal("repeated Close lost the flush failure")
	}
}

func TestStoreFlushLoop(t *testing.T) {
	pool := keyDB(t, false)
	id := keyUser(t, pool, "loop@example.com")
	s := dbStore(t, pool, testConfig())
	if err := s.Start(t.Context(), 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	_, token, err := s.Create(t.Context(), id, CreateInput{Name: "loop", Scope: ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	if serveKey(t, s, token) != http.StatusNoContent {
		t.Fatal("request failed")
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("flush loop did not persist the request")
		case <-poll.C:
			usage, err := s.Usage(t.Context(), id, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(usage) == 1 && usage[0].Count == 1 {
				if err := s.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
				pool.Close()
				if err := s.Close(t.Context()); err != nil {
					t.Fatalf("repeated Close touched the closed pool: %v", err)
				}
				return
			}
		}
	}
}
