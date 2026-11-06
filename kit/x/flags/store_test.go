package flags

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/audit"
	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/db"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

type storeLogs struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *storeLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *storeLogs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func storeFixture(t *testing.T, pool *pgxpool.Pool) (*Store, *Flags, *storeLogs) {
	t.Helper()
	logs := &storeLogs{}
	f, err := New(WithDeclared(testDeclared), WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(pool, f, auth.DefaultRoles()), f, logs
}

func lazyStorePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), "postgres://postgres:postgres@127.0.0.1:55433/postgres?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func requireStoreDB(t *testing.T) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is empty")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Skipf("PostgreSQL is unavailable: %v", err)
	}
	_ = conn.Close(ctx)
}

func flagMigrations(t *testing.T, withAudit bool) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	files := map[string][]string{
		"auth":  {"00002_create_auth.sql", "00005_users_role.sql", "00007_users_auth_version.sql"},
		"flags": {"00008_feature_flags.sql"},
	}
	if withAudit {
		files["audit"] = []string{"00006_audit_events.sql"}
	}
	for pack, names := range files {
		for _, name := range names {
			data, err := os.ReadFile("../../../internal/templates/packs/" + pack + "/files/api/db/kit/" + name)
			if err != nil {
				t.Fatal(err)
			}
			fsys[name] = &fstest.MapFile{Data: data, Mode: 0o644}
		}
	}
	return fsys
}

func flagDB(t *testing.T, withAudit bool) *pgxpool.Pool {
	t.Helper()
	requireStoreDB(t)
	return testkit.DBWithMigrations(t, fstest.MapFS{}, testkit.WithKitMigrations(flagMigrations(t, withAudit)))
}

func storeExec(t *testing.T, pool *pgxpool.Pool, sql string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), sql); err != nil {
		t.Fatal(err)
	}
}

func checkStoreError(t *testing.T, err, target error, text string) {
	t.Helper()
	if !errors.Is(err, target) || err.Error() != text {
		t.Fatalf("error = %v, want %v with text %q", err, target, text)
	}
}

func TestStoreValidation(t *testing.T) {
	s, _, _ := storeFixture(t, lazyStorePool(t))
	ctx := t.Context()
	unknown := "flags: the flag \"x\" is not declared. Declared flags: dark_mode, new_checkout.\nTry: add [flags.x] to spinup.toml and run spinup g flags."
	for _, write := range []func() error{
		func() error { return s.Enable(ctx, "x") },
		func() error { return s.Disable(ctx, "x") },
		func() error { return s.SetRoles(ctx, "x", nil) },
		func() error { return s.SetPercentage(ctx, "x", 1) },
		func() error { return s.SetRule(ctx, "x", Rule{}) },
		func() error { return s.SetUser(ctx, "x", "a@example.com", true) },
		func() error { return s.ClearUser(ctx, "x", "a@example.com") },
	} {
		checkStoreError(t, write(), ErrUnknownKey, unknown)
	}
	role := "flags: the role \"x\" is not declared. Declared roles: manager, staff.\nTry: use one of the declared roles, or add the role to [auth] in spinup.toml and run spinup g roles."
	checkStoreError(t, s.SetRoles(ctx, "new_checkout", []string{"x"}), ErrUnknownRole, role)
	checkStoreError(t, s.SetRule(ctx, "new_checkout", Rule{Roles: []string{"x"}}), ErrUnknownRole, role)
	for _, pct := range []int{-1, 120} {
		text := fmt.Sprintf("flags: the percentage %d is not from 0 to 100.\nTry: use a whole number from 0 to 100.", pct)
		checkStoreError(t, s.SetPercentage(ctx, "new_checkout", pct), ErrPercentage, text)
		checkStoreError(t, s.SetRule(ctx, "new_checkout", Rule{Percentage: pct}), ErrPercentage, text)
	}
}

func TestNewStorePanics(t *testing.T) {
	pool := lazyStorePool(t)
	for _, tc := range []struct {
		pool *pgxpool.Pool
		f    *Flags
	}{{nil, newTestFlags(t)}, {pool, nil}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("NewStore did not panic")
				}
			}()
			NewStore(tc.pool, tc.f, auth.DefaultRoles())
		}()
	}
}

func TestStoreStartConfigurationAndFailure(t *testing.T) {
	pool := lazyStorePool(t)
	pool.Close()
	s, f, logs := storeFixture(t, pool)
	if s.Ready(t.Context()) == nil {
		t.Fatal("Ready succeeded before a load")
	}
	err := s.Start(t.Context(), 500*time.Millisecond)
	if err == nil || err.Error() != "flags: the refresh interval 500ms is below 1s.\nTry: set FLAGS_REFRESH to 1s or more." {
		t.Fatalf("Start = %v", err)
	}
	if err := s.Start(t.Context(), time.Hour); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if s.Ready(t.Context()) == nil || !f.Snapshot().LoadedAt().IsZero() || !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("Ready = %v, logs = %s", s.Ready(t.Context()), logs.String())
	}
	if err := s.Start(t.Context(), time.Second); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("second Start = %v", err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestStoreSyncListAndPrune(t *testing.T) {
	pool := flagDB(t, false)
	s, f, logs := storeFixture(t, pool)
	ctx := t.Context()
	entries, err := s.List(ctx, time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC))
	if err != nil || len(entries) != 2 || !entries[0].Enabled || !entries[0].UpdatedAt.IsZero() {
		t.Fatalf("List before Sync = %+v, %v", entries, err)
	}
	data, err := json.Marshal(entries)
	if err != nil || !bytes.Contains(data, []byte(`"roles":[]`)) || !bytes.Contains(data, []byte(`"users":[]`)) {
		t.Fatalf("List JSON = %s, %v", data, err)
	}
	storeExec(t, pool, `INSERT INTO feature_flags (key, enabled, roles, percentage) VALUES ('new_checkout', true, '{staff}', 12), ('zombie', false, '{}', 0), ('abandoned', true, '{}', 0)`)
	storeExec(t, pool, `INSERT INTO users (email, password_hash) VALUES ('a@example.com', 'hash')`)
	storeExec(t, pool, `INSERT INTO feature_flag_users (flag_key, user_id, enabled) SELECT 'zombie', id, true FROM users`)
	var before time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM feature_flags WHERE key = 'new_checkout'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		stale, err := s.Sync(ctx)
		if err != nil || !reflect.DeepEqual(stale, []string{"abandoned", "zombie"}) {
			t.Fatalf("Sync = %v, %v", stale, err)
		}
	}
	if !strings.Contains(logs.String(), `has no declaration. Try: run`) {
		t.Fatalf("stale warning missing: %s", logs.String())
	}
	entries, err = s.List(ctx, time.Now())
	if err != nil || len(entries) != 4 {
		t.Fatalf("List = %+v, %v", entries, err)
	}
	if e := entries[2]; e.Key != "new_checkout" || !e.Enabled || e.Percentage != 12 || !reflect.DeepEqual(e.Roles, []string{"staff"}) || !e.UpdatedAt.Equal(before) {
		t.Fatalf("Sync overwrote a row: %+v", e)
	}
	if !entries[0].Stale || !entries[3].Stale {
		t.Fatal("stale entries are not marked")
	}
	if err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if f.Snapshot().LoadedAt().IsZero() || len(f.Snapshot().Keys()) != 4 {
		t.Fatal("Load omitted the stale rules or database time")
	}
	deleted, err := s.Prune(ctx)
	if err != nil || !reflect.DeepEqual(deleted, []string{"abandoned", "zombie"}) {
		t.Fatalf("Prune = %v, %v", deleted, err)
	}
	if len(f.Snapshot().Keys()) != 2 {
		t.Fatal("Prune did not refresh")
	}
	var rules int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM feature_flag_users`).Scan(&rules); err != nil || rules != 0 {
		t.Fatalf("cascaded rules = %d, %v", rules, err)
	}
}

func TestStoreWrites(t *testing.T) {
	pool := flagDB(t, false)
	s, f, _ := storeFixture(t, pool)
	ctx := t.Context()
	if _, err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	storeExec(t, pool, `INSERT INTO users (email, password_hash) VALUES ('z@example.com', 'hash'), ('a@example.com', 'hash')`)
	const key = "new_checkout"
	steps := []struct {
		write func() error
		check func(Rule) bool
	}{
		{func() error { return s.Enable(ctx, key) }, func(r Rule) bool { return r.Enabled }},
		{func() error { return s.Disable(ctx, key) }, func(r Rule) bool { return !r.Enabled }},
		{func() error { return s.SetRoles(ctx, key, []string{"staff", "manager", "staff"}) }, func(r Rule) bool { return reflect.DeepEqual(r.Roles, []string{"manager", "staff"}) }},
		{func() error { return s.SetPercentage(ctx, key, 100) }, func(r Rule) bool { return r.Percentage == 100 }},
		{func() error {
			return s.SetRule(ctx, key, Rule{Enabled: true, Roles: []string{"staff"}, Percentage: 50, Users: map[int64]bool{999: true}})
		}, func(r Rule) bool {
			return r.Enabled && r.Percentage == 50 && reflect.DeepEqual(r.Roles, []string{"staff"}) && len(r.Users) == 0
		}},
		{func() error { return s.SetUser(ctx, key, " Z@Example.COM ", true) }, func(r Rule) bool { return len(r.Users) == 1 && r.Users[1] }},
		{func() error { return s.SetUser(ctx, key, "z@example.com", false) }, func(r Rule) bool { on, ok := r.Users[1]; return ok && !on }},
		{func() error { return s.SetUser(ctx, key, "a@example.com", true) }, func(r Rule) bool { return len(r.Users) == 2 && r.Users[2] }},
	}
	for i, step := range steps {
		if err := step.write(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		r, ok := f.Snapshot().Rule(key)
		if !ok || !step.check(r) {
			t.Fatalf("step %d: rule = %+v", i, r)
		}
	}
	entries, err := s.List(ctx, time.Now())
	if err != nil || len(entries[1].Users) != 2 || entries[1].Users[0].Email != "a@example.com" || entries[1].Users[1].Email != "z@example.com" {
		t.Fatalf("List users = %+v, %v", entries, err)
	}
	for range 2 {
		if err := s.ClearUser(ctx, key, " Z@EXAMPLE.COM "); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := f.Snapshot().Rule(key)
	if _, ok := r.Users[1]; ok {
		t.Fatal("ClearUser did not refresh")
	}
	missing := "flags: no account has this email.\nTry: run \"user list\" to see the accounts."
	checkStoreError(t, s.SetUser(ctx, key, "missing@example.com", true), ErrUserNotFound, missing)
	checkStoreError(t, s.ClearUser(ctx, key, "missing@example.com"), ErrUserNotFound, missing)
}

func TestStoreFailedRefreshKeepsSnapshot(t *testing.T) {
	pool := flagDB(t, false)
	s, f, logs := storeFixture(t, pool)
	ctx := t.Context()
	if _, err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	old := f.Snapshot()
	storeExec(t, pool, `CREATE FUNCTION hide_rules() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN ALTER TABLE feature_flag_users RENAME TO hidden_rules; RETURN NULL; END $$`)
	storeExec(t, pool, `CREATE TRIGGER hide_rules AFTER UPDATE ON feature_flags FOR EACH STATEMENT EXECUTE FUNCTION hide_rules()`)
	if err := s.Enable(ctx, "new_checkout"); err != nil {
		t.Fatalf("a committed write returned a refresh failure: %v", err)
	}
	if f.Snapshot() != old || s.Ready(ctx) == nil || !strings.Contains(logs.String(), "The last snapshot stays in use.") {
		t.Fatalf("snapshot changed or readiness succeeded: %v, %s", s.Ready(ctx), logs.String())
	}
	var enabled bool
	if err := pool.QueryRow(ctx, `SELECT enabled FROM feature_flags WHERE key = 'new_checkout'`).Scan(&enabled); err != nil || !enabled {
		t.Fatalf("write did not commit: %v, %v", enabled, err)
	}
	storeExec(t, pool, `ALTER TABLE hidden_rules RENAME TO feature_flag_users`)
	if err := s.Refresh(ctx); err != nil || s.Ready(ctx) != nil || !f.Resolve("new_checkout", Subject{}).Value {
		t.Fatalf("recovery: %v", err)
	}
	if strings.Count(logs.String(), "level=INFO") != 1 {
		t.Fatalf("recovery log = %s", logs.String())
	}
	if err := s.Refresh(ctx); err != nil || strings.Count(logs.String(), "level=INFO") != 1 {
		t.Fatalf("repeated recovery log: %v, %s", err, logs.String())
	}
}

func TestStoreRunnerWithoutTables(t *testing.T) {
	requireStoreDB(t)
	pool := testkit.DB(t)
	s, f, logs := storeFixture(t, pool)
	if err := s.Start(t.Context(), time.Second); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if s.Ready(t.Context()) == nil || f.Resolve("dark_mode", Subject{}).Reason != ReasonDefault || !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("Ready = %v, logs = %s", s.Ready(t.Context()), logs.String())
	}
	m, err := db.NewMigrator(testkit.URL(t, pool), fstest.MapFS{}, db.WithKitMigrations(flagMigrations(t, false)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	if err := m.KitUp(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A tick loads external changes without another Sync call.
	storeExec(t, pool, `INSERT INTO feature_flags (key, enabled) VALUES ('new_checkout', true)`)
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for !f.Resolve("new_checkout", Subject{}).Value {
		select {
		case <-deadline.C:
			t.Fatal("the runner did not refresh after the migration")
		case <-tick.C:
		}
	}
	if err := s.Ready(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestStoreAuditActorAndEvents(t *testing.T) {
	pool := flagDB(t, true)
	s, _, _ := storeFixture(t, pool)
	ctx := audit.WithActor(t.Context(), audit.Actor{Kind: audit.KindOperator, Email: "operator:ops", Source: audit.SourceCLI})
	storeExec(t, pool, `INSERT INTO users (email, password_hash) VALUES ('target@example.com', 'hash')`)
	if _, err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Enable(ctx, "new_checkout"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUser(ctx, "new_checkout", "target@example.com", false); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearUser(ctx, "new_checkout", "target@example.com"); err != nil {
		t.Fatal(err)
	}
	events, err := audit.List(ctx, pool, audit.Filter{ActorEmail: "operator:ops"})
	if err != nil || len(events) != 5 {
		t.Fatalf("audit events = %+v, %v", events, err)
	}
	wantActions := []string{"delete", "create", "update", "create", "create"}
	for i, e := range events {
		if e.Action != wantActions[i] || e.Actor.Kind != audit.KindOperator || e.Actor.Source != audit.SourceCLI || e.OwnerID != 0 {
			t.Fatalf("event %d = %+v", i, e)
		}
		if i < 2 && (e.Table != "feature_flag_users" || e.RecordID != "new_checkout:1") {
			t.Fatalf("composite key = %+v", e)
		}
	}
	admin := audit.Actor{Kind: audit.KindUser, ID: 1, Email: "admin@example.com", Role: "manager", Source: audit.SourceAPI}
	adminCtx := audit.WithActor(t.Context(), admin)
	if err := s.SetPercentage(adminCtx, "new_checkout", 25); err != nil {
		t.Fatal(err)
	}
	adminEvents, err := audit.List(adminCtx, pool, audit.Filter{ActorEmail: admin.Email})
	if err != nil || len(adminEvents) != 1 || adminEvents[0].Actor != admin || adminEvents[0].Action != "update" {
		t.Fatalf("admin audit events = %+v, %v", adminEvents, err)
	}
	storeExec(t, pool, `INSERT INTO feature_flags (key) VALUES ('stale')`)
	deleted, err := s.Prune(ctx)
	if err != nil || !reflect.DeepEqual(deleted, []string{"stale"}) {
		t.Fatalf("audited Prune = %v, %v", deleted, err)
	}
	events, err = audit.List(ctx, pool, audit.Filter{ActorEmail: "operator:ops"})
	if err != nil || len(events) != 6 || events[0].Action != "delete" || events[0].Table != "feature_flags" || events[0].RecordID != "stale" {
		t.Fatalf("prune events = %+v, %v", events, err)
	}
}

func TestStoreDatabaseErrors(t *testing.T) {
	pool := lazyStorePool(t)
	pool.Close()
	s, f, _ := storeFixture(t, pool)
	ctx := t.Context()
	old := f.Snapshot()
	for name, write := range map[string]func() error{
		"enable":     func() error { return s.Enable(ctx, "new_checkout") },
		"disable":    func() error { return s.Disable(ctx, "new_checkout") },
		"roles":      func() error { return s.SetRoles(ctx, "new_checkout", nil) },
		"percentage": func() error { return s.SetPercentage(ctx, "new_checkout", 0) },
		"rule":       func() error { return s.SetRule(ctx, "new_checkout", Rule{}) },
		"user":       func() error { return s.SetUser(ctx, "new_checkout", "a@example.com", true) },
		"clear user": func() error { return s.ClearUser(ctx, "new_checkout", "a@example.com") },
		"prune":      func() error { _, err := s.Prune(ctx); return err },
		"sync":       func() error { _, err := s.Sync(ctx); return err },
		"load":       func() error { _, err := s.Load(ctx); return err },
		"list":       func() error { _, err := s.List(ctx, time.Now()); return err },
		"refresh":    func() error { return s.Refresh(ctx) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := write(); err == nil || !strings.HasPrefix(err.Error(), "flags:") || !strings.Contains(err.Error(), "\nTry:") {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if f.Snapshot() != old {
		t.Fatal("a failed operation replaced the snapshot")
	}
	if err := s.Close(ctx); err != nil {
		t.Fatalf("Close before Start = %v", err)
	}
}

func TestStoreListExpiration(t *testing.T) {
	pool := flagDB(t, false)
	declared := Declared{
		"expired": {Description: "Expired flag", Default: true, Public: true, Expires: "2026-11-05"},
		"today":   {Expires: "2026-11-06"},
	}
	f, err := New(WithDeclared(declared))
	if err != nil {
		t.Fatal(err)
	}
	s := NewStore(pool, f, auth.DefaultRoles())
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, synced := range []bool{false, true} {
		if synced {
			if _, err := s.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		entries, err := s.List(t.Context(), now)
		if err != nil || len(entries) != 2 {
			t.Fatalf("List = %+v, %v", entries, err)
		}
		e := entries[0]
		if e.Key != "expired" || !e.Expired || !e.Enabled || !e.Default || !e.Public || e.Stale || e.Description != "Expired flag" || e.Expires != "2026-11-05" || entries[1].Expired {
			t.Fatalf("List metadata = %+v", entries)
		}
		if e.UpdatedAt.IsZero() == synced {
			t.Fatalf("synced = %v, UpdatedAt = %v", synced, e.UpdatedAt)
		}
		data, err := json.Marshal(entries)
		if err != nil || bytes.Contains(data, []byte(`"roles":null`)) || bytes.Contains(data, []byte(`"users":null`)) {
			t.Fatalf("List JSON = %s, %v", data, err)
		}
	}
}

func TestStoreSyncRollback(t *testing.T) {
	pool := flagDB(t, false)
	s, f, _ := storeFixture(t, pool)
	storeExec(t, pool, `ALTER TABLE feature_flags ADD CONSTRAINT reject_checkout CHECK (key <> 'new_checkout')`)
	if _, err := s.Sync(t.Context()); err == nil {
		t.Fatal("Sync succeeded despite the rejected key")
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM feature_flags`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("Sync did not roll back: %d rows, %v", count, err)
	}
	if !f.Snapshot().LoadedAt().IsZero() {
		t.Fatal("a failed Sync refreshed the snapshot")
	}
}

func TestStoreRunnerStopsWithContext(t *testing.T) {
	pool := lazyStorePool(t)
	pool.Close()
	s, _, _ := storeFixture(t, pool)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := s.Start(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	cancel()
	closeCtx, closeCancel := context.WithTimeout(t.Context(), time.Second)
	defer closeCancel()
	if err := s.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
}

type loadQueryKey struct{}

type loadObserver struct {
	once       sync.Once
	afterFlags func(context.Context)
}

func (o *loadObserver) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, loadQueryKey{}, data.SQL)
}

func (o *loadObserver) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	sql, _ := ctx.Value(loadQueryKey{}).(string)
	if strings.HasPrefix(sql, "SELECT key, enabled, roles, percentage, updated_at FROM feature_flags") {
		o.once.Do(func() { o.afterFlags(ctx) })
	}
}

func TestStoreLoadUsesOneDatabaseSnapshot(t *testing.T) {
	pool := flagDB(t, false)
	s, f, _ := storeFixture(t, pool)
	ctx := t.Context()
	if _, err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	storeExec(t, pool, `INSERT INTO users (email, password_hash) VALUES ('target@example.com', 'hash')`)
	if err := s.SetUser(ctx, "new_checkout", "target@example.com", false); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(testkit.URL(t, pool))
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	cfg.ConnConfig.Tracer = &loadObserver{afterFlags: func(context.Context) {
		// Commit an external change between the two reads of Load.
		storeExec(t, pool, `UPDATE feature_flags SET enabled = true WHERE key = 'new_checkout'`)
		storeExec(t, pool, `UPDATE feature_flag_users SET enabled = true WHERE flag_key = 'new_checkout'`)
		changed = true
	}}
	observed, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer observed.Close()
	s = NewStore(observed, f, auth.DefaultRoles())
	snap, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rule, ok := snap.Rule("new_checkout")
	if !changed || !ok || rule.Enabled || len(rule.Users) != 1 || rule.Users[1] {
		t.Fatalf("Load mixed database states: %+v, changed = %v", rule, changed)
	}
	if snap.LoadedAt().IsZero() {
		t.Fatal("Load omitted the transaction time")
	}
	// A later transaction sees the committed external changes.
	snap, err = s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rule, _ = snap.Rule("new_checkout")
	if !rule.Enabled || !rule.Users[1] {
		t.Fatalf("later Load missed committed changes: %+v", rule)
	}
}

func TestStoreLoggerAndRolesCopy(t *testing.T) {
	pool := lazyStorePool(t)
	pool.Close()
	f := newTestFlags(t)
	logs := &storeLogs{}
	roles := auth.DefaultRoles()
	s := NewStore(pool, f, roles, WithStoreLogger(slog.New(slog.NewTextHandler(logs, nil))))
	roles.Names[0] = "intruder"
	if err := s.SetRoles(t.Context(), "new_checkout", []string{"intruder"}); !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("NewStore retained the caller's roles: %v", err)
	}
	if err := s.Refresh(t.Context()); err == nil || !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("custom logger: %v, %s", err, logs.String())
	}
}
