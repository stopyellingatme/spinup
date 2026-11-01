package audit_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/db"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

// The schema tests run the audit module migration through goose (the kit
// migrator) on the two auth shapes of ADR 0025: the todo shape (auth 00002 only)
// and the OIDC shape (auth 00002 to 00005). They read the migrations of the
// template packs by their paths in the repository.

const packs = "../../internal/templates/packs"

func packFile(t *testing.T, pack, name string) *fstest.MapFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(packs, pack, "files/api/db/migrations", name))
	if err != nil {
		t.Fatalf("read the %s migration %s: %v", pack, name, err)
	}
	return &fstest.MapFile{Data: data}
}

func sqlFile(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }

func schemaDB(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("set DATABASE_URL to run the audit schema tests")
	}
	pool := testkit.DB(t)
	return pool, testkit.URL(t, pool)
}

func migrate(t *testing.T, url string, fsys fstest.MapFS, to int64) {
	t.Helper()
	m, err := db.NewMigrator(url, fsys, db.WithOutput(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	ctx := context.Background()
	if to < 0 {
		err = m.Up(ctx)
	} else {
		err = m.DownTo(ctx, to)
	}
	if err != nil {
		t.Fatalf("migrate (to %d): %v", to, err)
	}
}

type row struct {
	Action, Table, RecordID, Kind, Source string
	Owner                                 *int64
	Changes                               string
	Email, Reason, RequestID              *string
}

// events returns the events after id, in order.
func events(t *testing.T, pool *pgxpool.Pool, after int64) []row {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT action, table_name, record_id, actor_kind, source, owner_id,
		changes::text, actor_email, reason, request_id FROM audit_events WHERE id > $1 ORDER BY id`, after)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var e row
		err := r.Scan(&e.Action, &e.Table, &e.RecordID, &e.Kind, &e.Source, &e.Owner, &e.Changes, &e.Email, &e.Reason, &e.RequestID)
		return e, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func lastID(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `SELECT coalesce(max(id), 0) FROM audit_events`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// execFails runs sql and wants an error that contains want.
func execFails(t *testing.T, pool *pgxpool.Pool, want, sql string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), sql)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("%s: error %v, want one with %q", sql, err, want)
	}
}

// asActor runs sql in a transaction with the actor setting, as audit.Bind does.
func asActor(t *testing.T, pool *pgxpool.Pool, actor, sql string) {
	t.Helper()
	ctx := context.Background()
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('spinup.audit', $1, true)`, actor); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, sql)
		return err
	})
	if err != nil {
		t.Fatalf("%s as %s: %v", sql, actor, err)
	}
}

func triggers(t *testing.T, pool *pgxpool.Pool, table string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT tgname FROM pg_trigger
		WHERE tgrelid = to_regclass($1) AND NOT tgisinternal ORDER BY tgname`, table)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func wantChanges(t *testing.T, got row, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got.Changes), &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad want %s: %v", want, err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Errorf("%s %s %s changes = %s, want %s", got.Action, got.Table, got.RecordID, gb, wb)
	}
}

func wantEvent(t *testing.T, got row, action, table, record, kind, source string) {
	t.Helper()
	if got.Action != action || got.Table != table || got.RecordID != record || got.Kind != kind || got.Source != source {
		t.Errorf("event = %s %s %s %s %s, want %s %s %s %s %s",
			got.Action, got.Table, got.RecordID, got.Kind, got.Source, action, table, record, kind, source)
	}
}

func wantNoAuditObjects(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM pg_proc WHERE proname LIKE 'audit\_%' AND pronamespace = 'public'::regnamespace) +
		(SELECT count(*) FROM pg_class WHERE relname = 'audit_events') +
		(SELECT count(*) FROM pg_trigger WHERE tgname LIKE '%\_audit%')`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("after down: %d audit objects remain, want 0", n)
	}
}

// todoShape is the todo app: init, auth 00002, a todos resource, and the audit module.
// 00007 attaches the todos triggers as spinup g audit does. 00008 is a data migration
// that binds an actor on its first line (ADR 0025 decision 4.10). 00009 binds none.
func todoShape(t *testing.T) fstest.MapFS {
	return fstest.MapFS{
		"00001_init.sql":        packFile(t, "api", "00001_init.sql"),
		"00002_create_auth.sql": packFile(t, "auth", "00002_create_auth.sql"),
		"00003_create_todos.sql": sqlFile(`-- +goose Up
CREATE TABLE todos (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title text NOT NULL,
    done boolean NOT NULL DEFAULT false,
    notes text,
    api_token text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO users (email, password_hash) VALUES ('seed@example.com', 'h');
INSERT INTO todos (user_id, title) VALUES (1, 'seed');

-- +goose Down
DROP TABLE todos;
`),
		"00006_audit_events.sql": packFile(t, "audit", "00006_audit_events.sql"),
		"00007_audit_todos.sql": sqlFile(`-- +goose Up
CREATE TRIGGER todos_audit AFTER INSERT OR UPDATE OR DELETE ON todos
    FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'id', 'api_token');
CREATE TRIGGER todos_audit_truncate AFTER TRUNCATE ON todos
    FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();

-- +goose Down
DROP TRIGGER IF EXISTS todos_audit_truncate ON todos;
DROP TRIGGER IF EXISTS todos_audit ON todos;
`),
		"00008_migrate_actor.sql": sqlFile(`-- +goose Up
SELECT set_config('spinup.audit', '{"kind":"system","source":"migrate","reason":"backfill"}', true);
UPDATE todos SET done = true;

-- +goose Down
SELECT 1;
`),
		"00009_migrate_plain.sql": sqlFile(`-- +goose Up
UPDATE todos SET title = title || '!';

-- +goose Down
SELECT 1;
`),
	}
}

func TestSchemaTodoShape(t *testing.T) {
	t.Parallel()
	pool, url := schemaDB(t)
	fsys := todoShape(t)
	migrate(t, url, fsys, -1)

	if got := strings.Join(triggers(t, pool, "users"), ","); got != "users_audit,users_audit_truncate" {
		t.Errorf("users triggers = %s", got)
	}
	var identities *string
	if err := pool.QueryRow(context.Background(), `SELECT to_regclass('public.user_identities')::text`).Scan(&identities); err != nil || identities != nil {
		t.Fatalf("user_identities = %v, %v; the todo shape has no such table", identities, err)
	}

	// Decision 4.10: the set_config line of a goose migration reaches the trigger.
	got := events(t, pool, 0)
	if len(got) != 2 {
		t.Fatalf("migration events = %+v, want 2", got)
	}
	wantEvent(t, got[0], "update", "todos", "1", "system", "migrate")
	wantChanges(t, got[0], `{"done":{"old":false,"new":true}}`)
	if got[0].Reason == nil || *got[0].Reason != "backfill" || got[0].Owner == nil || *got[0].Owner != 1 {
		t.Errorf("migration event reason %v owner %v", got[0].Reason, got[0].Owner)
	}
	wantEvent(t, got[1], "update", "todos", "1", "unattributed", "sql")

	t.Run("row changes", func(t *testing.T) {
		start := lastID(t, pool)
		user := `{"kind":"user","id":1,"email":"seed@example.com","role":"staff","source":"api","request_id":"req-1"}`
		asActor(t, pool, user, `INSERT INTO todos (user_id, title, api_token) VALUES (1, 'two', 'secret-1')`)
		asActor(t, pool, user, `UPDATE todos SET title = 'two b', api_token = 'secret-2', updated_at = now() WHERE id = 2`)
		exec(t, pool, `UPDATE todos SET updated_at = now() WHERE id = 2`) // no listed column changes: no event
		exec(t, pool, `UPDATE todos SET api_token = 'secret-2' WHERE id = 2`)
		exec(t, pool, `UPDATE todos SET notes = repeat('x', 5000) WHERE id = 2`)
		asActor(t, pool, user, `DELETE FROM todos WHERE id = 2`)
		exec(t, pool, `TRUNCATE todos`)
		got := events(t, pool, start)
		if len(got) != 5 {
			t.Fatalf("events = %+v, want 5", got)
		}
		wantEvent(t, got[0], "create", "todos", "2", "user", "api")
		wantChanges(t, got[0], `{"user_id":{"new":1},"title":{"new":"two"},"done":{"new":false},"api_token":{"redacted":true}}`)
		if got[0].Email == nil || *got[0].Email != "seed@example.com" || got[0].RequestID == nil || *got[0].RequestID != "req-1" {
			t.Errorf("insert actor email %v request %v", got[0].Email, got[0].RequestID)
		}
		wantEvent(t, got[1], "update", "todos", "2", "user", "api")
		wantChanges(t, got[1], `{"title":{"old":"two","new":"two b"},"api_token":{"redacted":true}}`)
		// A write after the transaction is unattributed: the setting does not leak.
		wantEvent(t, got[2], "update", "todos", "2", "unattributed", "sql")
		wantChanges(t, got[2], `{"notes":{"old":null,"new":{"truncated":true,"bytes":5000,"sha256":"`+sha5000x+`"}}}`)
		wantEvent(t, got[3], "delete", "todos", "2", "user", "api")
		wantChanges(t, got[3], `{"user_id":{"old":1},"title":{"old":"two b"},"done":{"old":false},"api_token":{"redacted":true},
			"notes":{"old":{"truncated":true,"bytes":5000,"sha256":"`+sha5000x+`"}}}`)
		wantEvent(t, got[4], "truncate", "todos", "*", "unattributed", "sql")
		if got[4].Owner != nil {
			t.Errorf("truncate owner = %d", *got[4].Owner)
		}
	})

	t.Run("users and redaction", func(t *testing.T) {
		start := lastID(t, pool)
		exec(t, pool, `INSERT INTO users (email, password_hash) VALUES ('ana@example.com', 'hash-1')`)
		exec(t, pool, `UPDATE users SET password_hash = 'hash-2' WHERE email = 'ana@example.com'`)
		got := events(t, pool, start)
		if len(got) != 2 {
			t.Fatalf("events = %+v, want 2", got)
		}
		wantChanges(t, got[0], `{"email":{"new":"ana@example.com"},"password_hash":{"redacted":true}}`)
		wantChanges(t, got[1], `{"password_hash":{"redacted":true}}`)
		if strings.Contains(got[0].Changes+got[1].Changes, "hash-") {
			t.Error("an event holds the password hash")
		}
	})

	t.Run("guard", func(t *testing.T) {
		execFails(t, pool, "UPDATE on audit_events is not allowed", `UPDATE audit_events SET reason = 'x'`)
		execFails(t, pool, "DELETE on audit_events is not allowed", `DELETE FROM audit_events`)
		execFails(t, pool, "TRUNCATE on audit_events is not allowed", `TRUNCATE audit_events`)
		execFails(t, pool, "violates check constraint", `INSERT INTO audit_events (action, table_name, record_id, actor_kind, source)
			VALUES ('Bad Action', 't', '1', 'user', 'api')`)
	})

	t.Run("failed trigger rolls back", func(t *testing.T) {
		exec(t, pool, `CREATE TABLE notes (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, body text)`)
		exec(t, pool, `CREATE TRIGGER notes_audit AFTER INSERT OR UPDATE OR DELETE ON notes
			FOR EACH ROW EXECUTE FUNCTION audit_row_change('', 'id,missing')`)
		execFails(t, pool, "the key column missing is not a column of notes", `INSERT INTO notes (body) VALUES ('x')`)
		var n int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM notes`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("notes rows = %d, %v; want 0", n, err)
		}
		exec(t, pool, `DROP TABLE notes`)
	})

	t.Run("prune", func(t *testing.T) {
		exec(t, pool, `INSERT INTO audit_events (occurred_at, action, table_name, record_id, actor_kind, source)
			VALUES ('2020-01-01T00:00:00Z', 'auth.sign_in', 'users', '1', 'user', 'api')`)
		execFails(t, pool, "younger than 1 day", `SELECT audit_prune(now(), 'retention')`)
		execFails(t, pool, "needs a reason", `SELECT audit_prune('2021-01-01T00:00:00Z', ' ')`)
		start := lastID(t, pool)
		var removed int64
		ctx := context.Background()
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('spinup.audit', '{"kind":"operator","email":"operator:ops","source":"cli"}', true)`); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT audit_prune('2021-01-01T00:00:00Z', 'retention')`).Scan(&removed); err != nil {
				return err
			}
			// The prune setting is off again after audit_prune.
			_, err := tx.Exec(ctx, `DELETE FROM audit_events`)
			if err == nil || !strings.Contains(err.Error(), "not allowed") {
				t.Errorf("DELETE after audit_prune in the same transaction: %v", err)
			}
			return nil
		})
		if err == nil {
			t.Fatal("the transaction with a failed DELETE committed")
		}
		// The failed DELETE aborted the transaction, so run the prune again alone.
		asActor(t, pool, `{"kind":"operator","email":"operator:ops","source":"cli"}`, `SELECT audit_prune('2021-01-01T00:00:00Z', 'retention')`)
		got := events(t, pool, start)
		if len(got) != 1 {
			t.Fatalf("prune events = %+v, want 1", got)
		}
		wantEvent(t, got[0], "audit.prune", "audit_events", "*", "operator", "cli")
		wantChanges(t, got[0], `{"before":"2021-01-01T00:00:00Z","count":1}`)
		if got[0].Reason == nil || *got[0].Reason != "retention" {
			t.Errorf("prune reason = %v", got[0].Reason)
		}
		var old int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE occurred_at < '2021-01-01'`).Scan(&old); err != nil || old != 0 {
			t.Errorf("old events = %d, %v; want 0", old, err)
		}
	})

	t.Run("audit_append", func(t *testing.T) {
		start := lastID(t, pool)
		exec(t, pool, `SELECT audit_append('auth.sign_in', 'users', '1', NULL, NULL, 'user', 1, 'seed@example.com', 'staff', 'api', NULL, 'req-9')`)
		exec(t, pool, `SELECT audit_append('export', 'todos', '*', NULL, '{}', '', NULL, '', '', '', '', '')`)
		got := events(t, pool, start)
		if len(got) != 2 {
			t.Fatalf("events = %+v", got)
		}
		wantEvent(t, got[0], "auth.sign_in", "users", "1", "user", "api")
		wantEvent(t, got[1], "export", "todos", "*", "unattributed", "sql")
		if got[1].Email != nil || got[1].Reason != nil {
			t.Errorf("empty strings are not NULL: %+v", got[1])
		}
	})

	t.Run("down and up", func(t *testing.T) {
		migrate(t, url, fsys, 5)
		wantNoAuditObjects(t, pool)
		migrate(t, url, fsys, 0)
		migrate(t, url, fsys, -1)
		if got := strings.Join(triggers(t, pool, "todos"), ","); got != "todos_audit,todos_audit_truncate" {
			t.Errorf("todos triggers after up = %s", got)
		}
	})
}

// sha5000x is the SHA-256 of 5000 times "x".
const sha5000x = "c59d3c0480cc2d71d8f646e735e92da65450311eec46e81a5db8c7e6e8a92054"

func oidcShape(t *testing.T) fstest.MapFS {
	return fstest.MapFS{
		"00001_init.sql":            packFile(t, "api", "00001_init.sql"),
		"00002_create_auth.sql":     packFile(t, "auth", "00002_create_auth.sql"),
		"00003_password_resets.sql": packFile(t, "auth", "00003_password_resets.sql"),
		"00004_oidc_identities.sql": packFile(t, "auth", "00004_oidc_identities.sql"),
		"00005_users_role.sql":      packFile(t, "auth", "00005_users_role.sql"),
		"00006_audit_events.sql":    packFile(t, "audit", "00006_audit_events.sql"),
	}
}

func TestSchemaOIDCShape(t *testing.T) {
	t.Parallel()
	pool, url := schemaDB(t)
	fsys := oidcShape(t)
	migrate(t, url, fsys, -1)
	if got := strings.Join(triggers(t, pool, "user_identities"), ","); got != "user_identities_audit,user_identities_audit_truncate" {
		t.Fatalf("user_identities triggers = %s", got)
	}

	exec(t, pool, `INSERT INTO users (email, password_hash, role) VALUES ('oidc@example.com', NULL, 'staff')`)
	exec(t, pool, `INSERT INTO user_identities (provider, subject, user_id, email) VALUES ('google', '1234567', 1, 'oidc@example.com')`)
	exec(t, pool, `UPDATE user_identities SET email = 'new@example.com'`)
	exec(t, pool, `TRUNCATE users, user_identities, password_resets`)
	got := events(t, pool, 0)
	if len(got) != 5 {
		t.Fatalf("events = %+v, want 5", got)
	}
	wantEvent(t, got[0], "create", "users", "1", "unattributed", "sql")
	wantChanges(t, got[0], `{"email":{"new":"oidc@example.com"},"role":{"new":"staff"}}`)
	wantEvent(t, got[1], "create", "user_identities", "google:1234567", "unattributed", "sql")
	wantChanges(t, got[1], `{"provider":{"new":"google"},"subject":{"new":"1234567"},"user_id":{"new":1},"email":{"new":"oidc@example.com"}}`)
	wantEvent(t, got[2], "update", "user_identities", "google:1234567", "unattributed", "sql")
	tables := got[3].Table + "," + got[4].Table
	if got[3].Action != "truncate" || got[4].Action != "truncate" || (tables != "users,user_identities" && tables != "user_identities,users") {
		t.Errorf("truncate events = %+v", got[3:])
	}

	migrate(t, url, fsys, 5)
	wantNoAuditObjects(t, pool)
	migrate(t, url, fsys, 0)
}

// TestSchemaOIDCAfterAudit adds OIDC (auth 00004) after the audit migration ran, out of
// order. The audit migration skipped user_identities, so the table has no trigger until
// spinup g audit user_identities attaches one (the audit-wired check warns).
func TestSchemaOIDCAfterAudit(t *testing.T) {
	t.Parallel()
	pool, url := schemaDB(t)
	fsys := oidcShape(t)
	delete(fsys, "00003_password_resets.sql")
	oidc := fsys["00004_oidc_identities.sql"]
	delete(fsys, "00004_oidc_identities.sql")
	migrate(t, url, fsys, -1)
	fsys["00004_oidc_identities.sql"] = oidc
	migrate(t, url, fsys, -1)
	if got := triggers(t, pool, "user_identities"); len(got) != 0 {
		t.Fatalf("user_identities triggers = %v, want none", got)
	}
	exec(t, pool, `INSERT INTO users (email, password_hash) VALUES ('a@example.com', NULL)`)
	exec(t, pool, `INSERT INTO user_identities (provider, subject, user_id, email) VALUES ('github', '42', 1, 'a@example.com')`)
	if got := events(t, pool, 0); len(got) != 1 || got[0].Table != "users" {
		t.Fatalf("events = %+v, want the users insert only", got)
	}
}
