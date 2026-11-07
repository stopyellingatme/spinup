package authsink_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/audit"
	"github.com/stopyellingatme/spinup/kit/audit/authsink"
	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/httpx"
	"github.com/stopyellingatme/spinup/kit/mail"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

const (
	packs    = "../../../internal/templates/packs"
	password = "correct horse battery"
)

func packFile(t *testing.T, pack, name string) *fstest.MapFile {
	t.Helper()
	// The api pack keeps 00001_init.sql in the app space. The other packs ship
	// their migrations in the kit space (ADR 0028).
	dir := "files/api/db/kit"
	if pack == "api" {
		dir = "files/api/db/migrations"
	}
	data, err := os.ReadFile(filepath.Join(packs, pack, dir, name))
	if err != nil {
		t.Fatalf("read the %s migration %s: %v", pack, name, err)
	}
	return &fstest.MapFile{Data: data}
}

// schema returns the migrations of an app with auth, mail (password reset),
// OIDC, and, with withAudit, the audit module.
func schema(t *testing.T, withAudit bool) fstest.MapFS {
	fsys := fstest.MapFS{
		"00001_init.sql":               packFile(t, "api", "00001_init.sql"),
		"00002_create_auth.sql":        packFile(t, "auth", "00002_create_auth.sql"),
		"00003_password_resets.sql":    packFile(t, "auth", "00003_password_resets.sql"),
		"00004_oidc_identities.sql":    packFile(t, "auth", "00004_oidc_identities.sql"),
		"00005_users_role.sql":         packFile(t, "auth", "00005_users_role.sql"),
		"00007_users_auth_version.sql": packFile(t, "auth", "00007_users_auth_version.sql"),
	}
	if withAudit {
		fsys["00006_audit_events.sql"] = packFile(t, "audit", "00006_audit_events.sql")
		fsys["00008_notes.sql"] = &fstest.MapFile{Data: []byte(`-- +goose Up
CREATE TABLE notes (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id),
    body text NOT NULL
);
CREATE TRIGGER notes_audit AFTER INSERT OR UPDATE OR DELETE ON notes
    FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'id');

-- +goose Down
DROP TABLE notes;
`)}
	}
	return fsys
}

type app struct {
	pool    *pgxpool.Pool
	auth    *auth.Auth
	handler http.Handler
	mail    *mail.Capture
}

// newApp starts auth with the audit sink on a new database, as main.go does.
func newApp(t *testing.T, withAudit bool) *app {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("set DATABASE_URL to run the authsink tests")
	}
	pool := testkit.DBWithMigrations(t, schema(t, withAudit))
	a, err := auth.New(pool,
		auth.WithEvents(authsink.New(pool)),
		auth.WithActorContext(authsink.ActorContext),
		auth.WithSecureCookie(false),
		auth.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	capture := &mail.Capture{}
	mux := http.NewServeMux()
	a.Routes(mux)
	if err := a.ResetRoutes(mux, auth.WithMailer(capture), auth.WithResetFrom("app@example.com"), auth.WithResetURL("https://app.example/reset")); err != nil {
		t.Fatal(err)
	}
	// A handwritten handler that follows the recipe of ADR 0025 decision 9.6.
	mux.Handle("POST /api/notes", a.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := auth.UserFrom(r.Context())
		err := audit.InTx(r.Context(), pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(r.Context(), `INSERT INTO notes (user_id, body) VALUES ($1, 'hello')`, user.ID)
			return err
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})))
	return &app{pool: pool, auth: a, handler: httpx.Chain(mux, httpx.RequestID(), a.Middleware()), mail: capture}
}

func (a *app) post(t *testing.T, path string, body any, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = strings.NewReader(string(data))
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://api.test"+path, r)
	req.Header.Set("X-Request-ID", "req-"+strings.ReplaceAll(strings.Trim(path, "/"), "/", "-"))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
	}
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	return rec
}

func cookieOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName && c.Value != "" {
			return c.Value
		}
	}
	t.Fatalf("no session cookie: %d %s", rec.Code, rec.Body)
	return ""
}

func creds(email, pw string) map[string]string {
	return map[string]string{"email": email, "password": pw}
}

// eventsAfter returns the events after id, oldest first.
func eventsAfter(t *testing.T, pool *pgxpool.Pool, id int64) []audit.Event {
	t.Helper()
	got, err := audit.List(context.Background(), pool, audit.Filter{BeforeID: 0, Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	var out []audit.Event
	for i := len(got) - 1; i >= 0; i-- {
		if got[i].ID > id {
			out = append(out, got[i])
		}
	}
	return out
}

func last(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `SELECT coalesce(max(id), 0) FROM audit_events`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// want checks one event. The subject of every event of these tests is the
// record 1: the account ana, or her first note.
func want(t *testing.T, e audit.Event, action, table string, actor audit.Actor) {
	t.Helper()
	const record = "1"
	if e.Action != action || e.Table != table || e.RecordID != record || e.Actor != actor {
		t.Errorf("event = %s %s %s %+v\nwant    %s %s %s %+v", e.Action, e.Table, e.RecordID, e.Actor, action, table, record, actor)
	}
}

func changes(t *testing.T, e audit.Event) map[string]map[string]any {
	t.Helper()
	var c map[string]map[string]any
	if err := json.Unmarshal(e.Changes, &c); err != nil {
		t.Fatalf("changes %s: %v", e.Changes, err)
	}
	return c
}

// TestAuthEvents sets the environment, so it does not run in parallel.
func TestAuthEvents(t *testing.T) {
	t.Setenv("AUTH_RATE_LIMIT", "off")
	t.Setenv("SUDO_USER", "")
	t.Setenv("USER", "kim")
	a := newApp(t, true)
	ctx := context.Background()

	// register: a users insert with the system actor, then a sign-in.
	reg := a.post(t, "/api/auth/register", creds("ana@example.com", password), "")
	if reg.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", reg.Code, reg.Body)
	}
	got := eventsAfter(t, a.pool, 0)
	if len(got) != 2 {
		t.Fatalf("register events = %+v", got)
	}
	want(t, got[0], "create", "users", audit.Actor{Kind: "system", Email: "ana@example.com", Role: "staff", Source: "api", Reason: "register", RequestID: "req-api-auth-register"})
	if c := changes(t, got[0]); c["email"]["new"] != "ana@example.com" || c["password_hash"]["redacted"] != true {
		t.Errorf("register changes = %s", got[0].Changes)
	}
	ana := audit.Actor{Kind: "user", ID: 1, Email: "ana@example.com", Role: "staff", Source: "api", RequestID: "req-api-auth-register"}
	want(t, got[1], "auth.sign_in", "users", ana)
	if string(got[1].Changes) != `{"method": "password"}` {
		t.Errorf("sign_in changes = %s", got[1].Changes)
	}

	// A handler with audit.InTx gets the signed-in actor from the middleware.
	start := last(t, a.pool)
	if r := a.post(t, "/api/notes", nil, cookieOf(t, reg)); r.Code != http.StatusCreated {
		t.Fatalf("notes: %d %s", r.Code, r.Body)
	}
	got = eventsAfter(t, a.pool, start)
	ana.RequestID = "req-api-notes"
	if len(got) != 1 {
		t.Fatalf("note events = %+v", got)
	}
	want(t, got[0], "create", "notes", ana)
	if got[0].OwnerID != 1 {
		t.Errorf("note owner = %d", got[0].OwnerID)
	}

	// sign-in and sign-out
	start = last(t, a.pool)
	login := a.post(t, "/api/auth/login", creds("ana@example.com", password), "")
	if login.Code != http.StatusOK {
		t.Fatalf("login: %d", login.Code)
	}
	if r := a.post(t, "/api/auth/logout", nil, cookieOf(t, login)); r.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", r.Code)
	}
	got = eventsAfter(t, a.pool, start)
	if len(got) != 2 {
		t.Fatalf("sign-in events = %+v", got)
	}
	ana.RequestID = "req-api-auth-login"
	want(t, got[0], "auth.sign_in", "users", ana)
	ana.RequestID = "req-api-auth-logout"
	want(t, got[1], "auth.sign_out", "users", ana)

	// lockout: one event for an existing account, none for an unknown email.
	start = last(t, a.pool)
	for range auth.DefaultLockoutThreshold + 1 {
		a.post(t, "/api/auth/login", creds("ana@example.com", "wrong password"), "")
		a.post(t, "/api/auth/login", creds("nobody@example.com", "wrong password"), "")
	}
	got = eventsAfter(t, a.pool, start)
	if len(got) != 1 {
		t.Fatalf("lockout events = %+v", got)
	}
	want(t, got[0], "auth.lockout", "users", audit.Actor{Kind: "system", Source: "api", Reason: "lockout", RequestID: "req-api-auth-login"})

	// The operator command: role_change with the operator actor and the reason.
	start = last(t, a.pool)
	cmd := auth.UserCommand(a.auth)
	stdout := os.Stdout
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	os.Stdout = devnull
	err := cmd(ctx, []string{"set-role", "--email", "ana@example.com", "--role", "manager", "--reason", "TICKET-7"})
	os.Stdout = stdout
	_ = devnull.Close()
	if err != nil {
		t.Fatal(err)
	}
	got = eventsAfter(t, a.pool, start)
	if len(got) != 1 {
		t.Fatalf("set-role events = %+v", got)
	}
	want(t, got[0], "update", "users", audit.Actor{Kind: "operator", Email: "operator:kim", Source: "cli", Reason: "TICKET-7"})
	if c := changes(t, got[0]); c["role"]["old"] != "staff" || c["role"]["new"] != "manager" {
		t.Errorf("set-role changes = %s", got[0].Changes)
	}

	// password reset: the account changes its own password.
	start = last(t, a.pool)
	if r := a.post(t, "/api/auth/reset/request", map[string]string{"email": "ana@example.com"}, ""); r.Code != http.StatusAccepted {
		t.Fatalf("reset request: %d", r.Code)
	}
	token := resetToken(t, a.mail)
	if r := a.post(t, "/api/auth/reset/confirm", map[string]string{"token": token, "password": "a new password 1"}, ""); r.Code != http.StatusNoContent {
		t.Fatalf("reset confirm: %d %s", r.Code, r.Body)
	}
	got = eventsAfter(t, a.pool, start)
	if len(got) != 1 {
		t.Fatalf("reset events = %+v", got)
	}
	want(t, got[0], "update", "users", audit.Actor{Kind: "user", ID: 1, Email: "ana@example.com", Role: "manager", Source: "api", Reason: "password_reset", RequestID: "req-api-auth-reset-confirm"})
	if string(got[0].Changes) != `{"auth_version": {"new": 2, "old": 1}, "password_hash": {"redacted": true}}` {
		t.Errorf("reset changes = %s", got[0].Changes)
	}
	if strings.Contains(string(got[0].Changes), "argon2") {
		t.Error("an event holds the password hash")
	}
}

func resetToken(t *testing.T, c *mail.Capture) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range c.Messages() {
			for _, word := range strings.Fields(m.Text) {
				if token, ok := strings.CutPrefix(word, "https://app.example/reset/"); ok {
					return token
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no reset mail")
	return ""
}

// TestOldSchema runs a new binary with the sink on a database without the audit
// migration: sign-in fails open, and row writes work (ADR 0025 decision 12).
func TestOldSchema(t *testing.T) {
	t.Setenv("AUTH_RATE_LIMIT", "off")
	a := newApp(t, false)
	reg := a.post(t, "/api/auth/register", creds("ana@example.com", password), "")
	if reg.Code != http.StatusCreated {
		t.Fatalf("register on the old schema: %d %s", reg.Code, reg.Body)
	}
	if r := a.post(t, "/api/auth/login", creds("ana@example.com", password), ""); r.Code != http.StatusOK {
		t.Fatalf("login on the old schema: %d %s", r.Code, r.Body)
	}
	if err := a.auth.SetRole(context.Background(), "ana@example.com", "manager"); err != nil {
		t.Fatalf("SetRole on the old schema: %v", err)
	}
}

func TestSinkUnits(t *testing.T) {
	t.Parallel()
	sink := authsink.New(nil)
	if err := sink(context.Background(), nil, auth.Event{Name: auth.EventSignIn}); err == nil || !strings.Contains(err.Error(), "\nTry:") {
		t.Errorf("a sink without a pool = %v", err)
	}
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := authsink.New(pool)(context.Background(), pool, auth.Event{Name: "unknown"}); err == nil || !strings.Contains(err.Error(), `unknown auth event "unknown"`) {
		t.Errorf("an unknown event = %v", err)
	}
	// A row event needs a transaction: Bind refuses the pool.
	if err := authsink.New(pool)(context.Background(), pool, auth.Event{Name: auth.EventRoleChange}); !errors.Is(err, audit.ErrNotInTx) {
		t.Errorf("a row event on a pool = %v, want ErrNotInTx", err)
	}
	ctx := authsink.ActorContext(context.Background(), auth.User{ID: 7, Email: "a@example.com", Role: "staff"})
	if got, ok := audit.ActorFrom(ctx); !ok || got != (audit.Actor{Kind: "user", ID: 7, Email: "a@example.com", Role: "staff", Source: "api"}) {
		t.Errorf("ActorContext = %+v, %v", got, ok)
	}
}
