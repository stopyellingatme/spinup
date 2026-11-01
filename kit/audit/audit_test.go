package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/audit"
)

// auditDB returns a pool on a new database in the todo shape (schema_test.go):
// users and todos with audit triggers, and one seed todo with the id 1.
func auditDB(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	pool, url := schemaDB(t)
	migrate(t, url, todoShape(t), -1)
	return pool, url
}

var ana = audit.Actor{Kind: audit.KindUser, ID: 1, Email: "ana@example.com", Role: "manager", Source: audit.SourceAPI, RequestID: "req-7"}

func TestBindRejectsPoolAndConn(t *testing.T) {
	t.Parallel()
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for name, q := range map[string]audit.Execer{"pool": pool, "conn": &pgx.Conn{}} {
		if err := audit.Bind(context.Background(), q); !errors.Is(err, audit.ErrNotInTx) || !strings.Contains(err.Error(), "\nTry:") {
			t.Errorf("Bind(%s) = %v, want ErrNotInTx with a Try: line", name, err)
		}
	}
	if err := audit.Bind(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "\nTry:") {
		t.Errorf("Bind(nil) = %v", err)
	}
	if err := audit.InTx(context.Background(), nil, func(pgx.Tx) error { return nil }); err == nil || !strings.Contains(err.Error(), "\nTry:") {
		t.Errorf("InTx(nil) = %v", err)
	}
}

func TestFilterValidate(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		f  audit.Filter
		ok bool
	}{
		{audit.Filter{}, true},
		{audit.Filter{Limit: audit.MaxLimit, Offset: audit.MaxOffset}, true},
		{audit.Filter{Limit: audit.MaxLimit + 1}, false},
		{audit.Filter{Limit: -1}, false},
		{audit.Filter{Offset: audit.MaxOffset + 1}, false},
		{audit.Filter{Offset: -1}, false},
	} {
		err := tt.f.Validate()
		if (err == nil) != tt.ok {
			t.Errorf("Validate(%+v) = %v", tt.f, err)
		}
		if err != nil && (!errors.Is(err, audit.ErrFilterLimit) || !strings.Contains(err.Error(), "\nTry:")) {
			t.Errorf("Validate(%+v) = %v, want ErrFilterLimit with a Try: line", tt.f, err)
		}
		// List checks the filter before it uses the database.
		if _, err := audit.List(context.Background(), nil, tt.f); !tt.ok && !errors.Is(err, audit.ErrFilterLimit) {
			t.Errorf("List(%+v) = %v", tt.f, err)
		}
	}
}

func TestOperatorAndSystem(t *testing.T) {
	t.Setenv("SUDO_USER", "")
	t.Setenv("USER", "kim")
	if got := audit.Operator(); got != (audit.Actor{Kind: audit.KindOperator, Source: audit.SourceCLI, Email: "operator:kim"}) {
		t.Errorf("Operator() = %+v", got)
	}
	t.Setenv("SUDO_USER", "root-kim")
	if got := audit.Operator().Email; got != "operator:root-kim" {
		t.Errorf("Operator().Email with SUDO_USER = %q", got)
	}
	t.Setenv("SUDO_USER", "")
	t.Setenv("USER", "")
	if got := audit.Operator().Email; got != "operator:unknown" {
		t.Errorf("Operator().Email without a login = %q", got)
	}
	if got := audit.System(audit.SourceJob, "send_mail"); got.Kind != audit.KindSystem || got.Source != "job" || got.Reason != "send_mail" {
		t.Errorf("System = %+v", got)
	}
}

func TestInTx(t *testing.T) {
	t.Parallel()
	pool, _ := auditDB(t)
	ctx := audit.WithActor(context.Background(), ana)

	start := lastID(t, pool)
	err := audit.InTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE todos SET title = 'bound' WHERE id = 1`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// A write outside InTx is unattributed.
	exec(t, pool, `UPDATE todos SET title = 'plain' WHERE id = 1`)
	// A job context through InTx writes the system actor (decision 4.9).
	jobCtx := audit.WithActor(context.Background(), audit.System(audit.SourceJob, "send_digest"))
	if err := audit.InTx(jobCtx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(jobCtx, `UPDATE todos SET title = 'job' WHERE id = 1`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A context without an actor binds nothing: the event is unattributed.
	if err := audit.InTx(context.Background(), pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE todos SET title = 'none' WHERE id = 1`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// A failing fn rolls the change and its event back.
	boom := errors.New("boom")
	if err := audit.InTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE todos SET title = 'rolled back' WHERE id = 1`); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("InTx = %v, want boom", err)
	}

	got := events(t, pool, start)
	if len(got) != 4 {
		t.Fatalf("events = %+v, want 4", got)
	}
	wantEvent(t, got[0], "update", "todos", "1", "user", "api")
	wantChanges(t, got[0], `{"title":{"old":"seed!","new":"bound"}}`)
	if got[0].Email == nil || *got[0].Email != "ana@example.com" || got[0].RequestID == nil || *got[0].RequestID != "req-7" || got[0].Owner == nil || *got[0].Owner != 1 {
		t.Errorf("bound event = %+v", got[0])
	}
	wantEvent(t, got[1], "update", "todos", "1", "unattributed", "sql")
	wantEvent(t, got[2], "update", "todos", "1", "system", "job")
	if got[2].Reason == nil || *got[2].Reason != "send_digest" {
		t.Errorf("job event reason = %v", got[2].Reason)
	}
	wantEvent(t, got[3], "update", "todos", "1", "unattributed", "sql")
	var title string
	if err := pool.QueryRow(context.Background(), `SELECT title FROM todos WHERE id = 1`).Scan(&title); err != nil || title != "none" {
		t.Errorf("title = %q, %v; the failed InTx did not roll back", title, err)
	}
}

// TestBindDoesNotLeak runs on a pool with one connection, so the next use gets
// the connection of the bound transaction.
func TestBindDoesNotLeak(t *testing.T) {
	t.Parallel()
	_, url := auditDB(t)
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := audit.WithActor(context.Background(), ana)
	var pid1, pid2 int
	var inside string
	if err := audit.InTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT pg_backend_pid(), current_setting('spinup.audit')`).Scan(&pid1, &inside)
	}); err != nil {
		t.Fatal(err)
	}
	var after *string
	if err := pool.QueryRow(context.Background(), `SELECT pg_backend_pid(), nullif(current_setting('spinup.audit', true), '')`).Scan(&pid2, &after); err != nil {
		t.Fatal(err)
	}
	if pid1 != pid2 {
		t.Fatalf("the pool used two connections (%d, %d); the test needs one", pid1, pid2)
	}
	var bound audit.Actor
	if err := json.Unmarshal([]byte(inside), &bound); err != nil || bound != ana {
		t.Errorf("setting inside the transaction = %s, %v", inside, err)
	}
	if after != nil {
		t.Errorf("the setting leaked to the next use of the connection: %s", *after)
	}
	start := lastID(t, pool)
	if _, err := pool.Exec(context.Background(), `UPDATE todos SET done = NOT done WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if got := events(t, pool, start); len(got) != 1 || got[0].Kind != "unattributed" {
		t.Errorf("events after the transaction = %+v, want one unattributed", got)
	}
}

func TestRecord(t *testing.T) {
	t.Parallel()
	pool, _ := auditDB(t)
	ctx := context.Background()
	start := lastID(t, pool)
	// An explicit actor on a pool (Record reads no setting).
	if err := audit.Record(ctx, pool, audit.Event{Action: "report.render", Table: "todos", RecordID: "1", OwnerID: 1,
		Changes: json.RawMessage(`{"template":"todo"}`), Actor: ana}); err != nil {
		t.Fatal(err)
	}
	// The actor of the context.
	if err := audit.Record(audit.WithActor(ctx, audit.Operator()), pool, audit.Event{Action: "export", Table: "todos", RecordID: "*"}); err != nil {
		t.Fatal(err)
	}
	// No actor at all.
	if err := audit.Record(ctx, pool, audit.Event{Action: "export", Table: "todos", RecordID: "*"}); err != nil {
		t.Fatal(err)
	}
	got := events(t, pool, start)
	if len(got) != 3 {
		t.Fatalf("events = %+v", got)
	}
	wantEvent(t, got[0], "report.render", "todos", "1", "user", "api")
	wantChanges(t, got[0], `{"template":"todo"}`)
	if got[0].Owner == nil || *got[0].Owner != 1 || *got[0].Email != "ana@example.com" {
		t.Errorf("event = %+v", got[0])
	}
	wantEvent(t, got[1], "export", "todos", "*", "operator", "cli")
	wantEvent(t, got[2], "export", "todos", "*", "unattributed", "sql")
	wantChanges(t, got[2], `{}`)

	for _, e := range []audit.Event{
		{Action: "Bad Action", Table: "todos", RecordID: "1"},
		{Action: "export", RecordID: "1"},
		{Action: "export", Table: "todos"},
		{Action: "export", Table: "todos", RecordID: "1", Changes: json.RawMessage(`{bad`)},
	} {
		if err := audit.Record(ctx, pool, e); err == nil || !strings.Contains(err.Error(), "\nTry:") {
			t.Errorf("Record(%+v) = %v, want an error with a Try: line", e, err)
		}
	}
	if err := audit.Record(ctx, nil, audit.Event{Action: "export", Table: "t", RecordID: "*"}); err == nil {
		t.Error("Record(nil) succeeded")
	}
}

func TestList(t *testing.T) {
	t.Parallel()
	pool, _ := auditDB(t)
	ctx := context.Background()
	exec(t, pool, `INSERT INTO users (email, password_hash) VALUES ('ben@example.com', 'h')`)
	exec(t, pool, `INSERT INTO todos (user_id, title) VALUES (2, 'ben')`)
	user := audit.WithActor(ctx, ana)
	for _, title := range []string{"a", "b", "c"} {
		if err := audit.InTx(user, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE todos SET title = $1 WHERE id = 1`, title)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}

	all, err := audit.List(ctx, pool, audit.Filter{Table: "todos", RecordID: "1"})
	if err != nil {
		t.Fatal(err)
	}
	// The migrations wrote 2 events for todo 1, then 3 updates.
	if len(all) != 5 {
		t.Fatalf("events = %d, want 5", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i].ID >= all[i-1].ID {
			t.Fatal("the events are not newest first")
		}
	}
	e := all[0]
	if e.Action != "update" || e.Table != "todos" || e.RecordID != "1" || e.OwnerID != 1 || e.Actor != ana || e.OccurredAt.IsZero() || e.OccurredAt.Location() != time.UTC {
		t.Errorf("newest event = %+v", e)
	}
	var changes map[string]map[string]string
	if err := json.Unmarshal(e.Changes, &changes); err != nil || changes["title"]["old"] != "b" || changes["title"]["new"] != "c" {
		t.Errorf("changes = %s, %v", e.Changes, err)
	}

	for _, tt := range []struct {
		name string
		f    audit.Filter
		want int
	}{
		{"limit", audit.Filter{Table: "todos", RecordID: "1", Limit: 2}, 2},
		{"cursor", audit.Filter{Table: "todos", RecordID: "1", BeforeID: all[1].ID}, 3},
		{"offset", audit.Filter{Table: "todos", RecordID: "1", Offset: 4}, 1},
		{"owner", audit.Filter{Table: "todos", OwnerID: 2}, 1},
		{"other owner of record 1", audit.Filter{Table: "todos", RecordID: "1", OwnerID: 2}, 0},
		{"actor", audit.Filter{ActorEmail: "ana@example.com"}, 3},
		{"action", audit.Filter{Table: "users", Action: "create"}, 1},
		{"unattributed", audit.Filter{Table: "todos", Unattributed: true}, 2},
		{"since", audit.Filter{Table: "todos", Since: e.OccurredAt}, 1},
		{"until", audit.Filter{Table: "todos", RecordID: "1", Until: all[3].OccurredAt.Add(time.Microsecond)}, 2},
	} {
		got, err := audit.List(ctx, pool, tt.f)
		if err != nil || len(got) != tt.want {
			t.Errorf("%s: %d events, %v; want %d", tt.name, len(got), err, tt.want)
		}
	}
	if _, err := audit.List(ctx, pool, audit.Filter{Limit: audit.MaxLimit + 1}); !errors.Is(err, audit.ErrFilterLimit) {
		t.Errorf("List over the limit = %v", err)
	}
}

func TestPrune(t *testing.T) {
	t.Parallel()
	pool, _ := auditDB(t)
	ctx := audit.WithActor(context.Background(), audit.Operator())
	exec(t, pool, `INSERT INTO audit_events (occurred_at, action, table_name, record_id, actor_kind, source)
		VALUES ('2020-01-01T00:00:00Z', 'auth.sign_in', 'users', '1', 'user', 'api'),
		       ('2020-06-01T00:00:00Z', 'auth.sign_in', 'users', '1', 'user', 'api')`)

	if _, err := audit.Prune(ctx, pool, time.Now().Add(-time.Hour), "retention"); err == nil ||
		!strings.Contains(err.Error(), "younger than 1 day") || !strings.Contains(err.Error(), "\nTry: pass a time at least 1 day in the past") {
		t.Errorf("Prune of a recent time = %v", err)
	}
	if _, err := audit.Prune(ctx, pool, time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), " "); err == nil || !strings.Contains(err.Error(), "\nTry:") {
		t.Errorf("Prune without a reason = %v", err)
	}
	if _, err := audit.Prune(ctx, nil, time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), "x"); err == nil {
		t.Error("Prune(nil pool) succeeded")
	}
	start := lastID(t, pool)
	n, err := audit.Prune(ctx, pool, time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), "retention")
	if err != nil || n != 2 {
		t.Fatalf("Prune = %d, %v; want 2", n, err)
	}
	got := events(t, pool, start)
	if len(got) != 1 {
		t.Fatalf("events = %+v", got)
	}
	wantEvent(t, got[0], "audit.prune", "audit_events", "*", "operator", "cli")
	wantChanges(t, got[0], `{"before":"2021-01-01T00:00:00Z","count":2}`)

	// The guard still rejects a plain delete.
	execFails(t, pool, "DELETE on audit_events is not allowed", `DELETE FROM audit_events`)
}
