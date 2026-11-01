package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/mail"
)

// The tests of this file use a recording sink. kit/auth never imports kit/audit,
// so the tests of the real sink with the audit trail are in kit/audit/authsink.

// seen is one call of the recording sink.
type seen struct {
	Event
	inTx, onPool bool
}

type recorder struct {
	mu    sync.Mutex
	calls []seen
	fail  func(Event) error
}

func (r *recorder) sink(ctx context.Context, q Execer, e Event) error {
	_, inTx := q.(pgx.Tx)
	_, onPool := q.(*pgxpool.Pool)
	r.mu.Lock()
	r.calls = append(r.calls, seen{Event: e, inTx: inTx, onPool: onPool})
	fail := r.fail
	r.mu.Unlock()
	if fail != nil {
		return fail(e)
	}
	if inTx {
		// Bind an actor as the audit sink does, so a broken transaction shows.
		_, err := q.Exec(ctx, `SELECT set_config('spinup.audit', $1, true)`, `{"kind":"system","reason":"`+e.Name+`"}`)
		return err
	}
	return nil
}

func (r *recorder) take() []seen {
	r.mu.Lock()
	defer r.mu.Unlock()
	calls := r.calls
	r.calls = nil
	return calls
}

func (r *recorder) setFail(f func(Event) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fail = f
}

func names(calls []seen) string {
	var out []string
	for _, c := range calls {
		out = append(out, c.Name)
	}
	return strings.Join(out, ",")
}

func TestEventsOfTheHandlers(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	var actors sync.Map
	f := newFixture(t, WithEvents(rec.sink), WithActorContext(func(ctx context.Context, u User) context.Context {
		actors.Store(u.Email, true)
		return context.WithValue(ctx, actorTestKey{}, u.Email)
	}))

	cookie := f.register(t, "ana@example.com")
	calls := rec.take()
	if names(calls) != "register,sign_in" || !calls[0].inTx || calls[0].User.Email != "ana@example.com" || calls[0].User.Role != "staff" || calls[0].User.ID != 0 {
		t.Fatalf("register calls = %+v", calls)
	}
	if !calls[1].onPool || calls[1].Method != "password" || calls[1].User.ID == 0 {
		t.Fatalf("sign_in call = %+v", calls[1])
	}

	// The middleware calls the actor function for a signed-in request.
	if r := f.do(t, request{method: http.MethodGet, path: "/api/private", cookie: cookie}); r.Code != http.StatusOK {
		t.Fatalf("private: %d", r.Code)
	}
	if _, ok := actors.Load("ana@example.com"); !ok {
		t.Error("the middleware did not call the WithActorContext function")
	}

	login := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("ana@example.com", goodPassword)})
	if login.Code != http.StatusOK {
		t.Fatalf("login: %d", login.Code)
	}
	if calls := rec.take(); names(calls) != "sign_in" || calls[0].Method != "password" || calls[0].User.Email != "ana@example.com" {
		t.Fatalf("login calls = %+v", calls)
	}
	logout := f.do(t, request{method: http.MethodPost, path: "/api/auth/logout", cookie: sessionCookie(login).Value})
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", logout.Code)
	}
	if calls := rec.take(); names(calls) != "sign_out" || calls[0].User.Email != "ana@example.com" || !calls[0].onPool {
		t.Fatalf("logout calls = %+v", calls)
	}
	// A logout without a user writes nothing.
	f.do(t, request{method: http.MethodPost, path: "/api/auth/logout"})
	if calls := rec.take(); len(calls) != 0 {
		t.Fatalf("anonymous logout calls = %+v", calls)
	}

	// Lockout: once, at the failure that reaches the threshold.
	for range DefaultLockoutThreshold + 2 {
		f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("ana@example.com", "incorrect password")})
	}
	if calls := rec.take(); names(calls) != "lockout" || calls[0].User.Email != "ana@example.com" || !calls[0].onPool {
		t.Fatalf("lockout calls = %+v", calls)
	}
	// An unknown email writes nothing.
	for range DefaultLockoutThreshold + 1 {
		f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("nobody@example.com", "incorrect password")})
	}
	if calls := rec.take(); len(calls) != 0 {
		t.Fatalf("unknown email calls = %+v", calls)
	}
}

type actorTestKey struct{}

func TestEventsRehash(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	f := newFixture(t, WithEvents(rec.sink))
	f.register(t, "old@example.com")
	rec.take()
	f.auth.params.time = testParams.time + 1
	if r := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("old@example.com", goodPassword)}); r.Code != http.StatusOK {
		t.Fatalf("login: %d", r.Code)
	}
	calls := rec.take()
	if names(calls) != "password_rehash,sign_in" || !calls[0].inTx || calls[0].User.Email != "old@example.com" {
		t.Fatalf("calls = %+v", calls)
	}
	var hash string
	if err := f.pool.QueryRow(t.Context(), `SELECT password_hash FROM users WHERE email = 'old@example.com'`).Scan(&hash); err != nil || !strings.Contains(hash, ",t=2,") {
		t.Fatalf("hash = %q, %v; want the new parameters", hash, err)
	}
}

func TestEventsFailClosedForRowChanges(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	f := newFixture(t, WithEvents(rec.sink))
	if _, err := f.auth.CreateUser(t.Context(), "boss@example.com", goodPassword, "manager"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.auth.CreateUser(t.Context(), "cook@example.com", goodPassword, "staff"); err != nil {
		t.Fatal(err)
	}
	rec.take()
	rec.setFail(func(Event) error { return errors.New("sink down") })

	if err := f.auth.SetRole(t.Context(), "cook@example.com", "manager"); err == nil || !strings.Contains(err.Error(), "sink down") || !strings.Contains(err.Error(), "\nTry:") {
		t.Fatalf("SetRole with a failing sink = %v", err)
	}
	var role string
	if err := f.pool.QueryRow(t.Context(), `SELECT role FROM users WHERE email = 'cook@example.com'`).Scan(&role); err != nil || role != "staff" {
		t.Fatalf("role = %q, %v; the role change did not roll back", role, err)
	}
	if _, err := f.auth.CreateUser(t.Context(), "new@example.com", goodPassword, "staff"); err == nil {
		t.Fatal("CreateUser with a failing sink succeeded")
	}
	if r := f.do(t, request{method: http.MethodPost, path: "/api/auth/register", body: creds("reg@example.com", goodPassword)}); r.Code != http.StatusInternalServerError {
		t.Fatalf("register with a failing sink: %d", r.Code)
	}
	if _, err := f.auth.BackfillRoles(t.Context()); err == nil {
		t.Fatal("BackfillRoles with a failing sink succeeded")
	}
	var n int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM users WHERE email IN ('new@example.com', 'reg@example.com')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("users = %d, %v; the inserts did not roll back", n, err)
	}
	if calls := rec.take(); names(calls) != "role_change,user_create,register,role_change" {
		t.Fatalf("calls = %s", names(calls))
	}
}

func TestEventsFailOpenForSignIn(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	var logs bytes.Buffer
	f := newFixture(t, WithEvents(rec.sink), WithLogger(slog.New(slog.NewTextHandler(&syncWriter{w: &logs}, nil))))
	f.register(t, "ana@example.com")
	rec.setFail(func(e Event) error {
		if e.Name == EventSignIn || e.Name == EventSignOut || e.Name == EventLockout {
			return errors.New("relation \"audit_events\" does not exist")
		}
		return nil
	})
	login := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("ana@example.com", goodPassword)})
	if login.Code != http.StatusOK || sessionCookie(login) == nil {
		t.Fatalf("login with a failing sink: %d %s", login.Code, login.Body)
	}
	if r := f.do(t, request{method: http.MethodPost, path: "/api/auth/logout", cookie: sessionCookie(login).Value}); r.Code != http.StatusNoContent {
		t.Fatalf("logout with a failing sink: %d", r.Code)
	}
	if !strings.Contains(logs.String(), "cannot record an account event") || !strings.Contains(logs.String(), "event=sign_in") {
		t.Fatalf("logs = %s", logs.String())
	}
}

// syncWriter makes a buffer safe for the logger of concurrent handlers.
type syncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// TestUserCommandOperator sets USER, so it does not run in parallel.
func TestUserCommandOperator(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL is not set")
	}
	t.Setenv("SUDO_USER", "")
	t.Setenv("USER", "kim")
	rec := &recorder{}
	f := newFixture(t, WithEvents(rec.sink))
	var out bytes.Buffer
	run := func(args []string, in string) {
		t.Helper()
		if err := runUserCommand(t.Context(), f.auth, args, strings.NewReader(in), &out); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	run([]string{"create", "--email", "boss@example.com", "--role", "manager", "--password-stdin", "--reason", "first manager"}, goodPassword+"\n")
	run([]string{"create", "--email", "cook@example.com", "--role", "staff", "--password-stdin"}, goodPassword+"\n")
	run([]string{"set-role", "--email", "cook@example.com", "--role", "manager", "--reason", "TICKET-7"}, "")
	run([]string{"set-role", "--email", "cook@example.com", "--role", "staff"}, "")
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO users (email, password_hash, role) VALUES ('legacy@example.com', 'h', '')`); err != nil {
		t.Fatal(err)
	}
	run([]string{"backfill"}, "")
	calls := rec.take()
	if names(calls) != "user_create,user_create,role_change,role_change,role_change" {
		t.Fatalf("calls = %s", names(calls))
	}
	for i, want := range []string{"first manager", "", "TICKET-7", "", "backfill"} {
		if calls[i].Actor != "operator:kim" || calls[i].Reason != want || !calls[i].inTx {
			t.Errorf("call %d = %+v, want the actor operator:kim and the reason %q", i, calls[i], want)
		}
	}
	if calls[2].User.Email != "cook@example.com" || calls[2].User.Role != "staff" || calls[2].User.ID == 0 {
		t.Errorf("role_change user = %+v, want the account before the change", calls[2].User)
	}
	// Without the command, CreateUser gives no operator.
	if _, err := f.auth.CreateUser(t.Context(), "code@example.com", goodPassword, "staff"); err != nil {
		t.Fatal(err)
	}
	if calls := rec.take(); len(calls) != 1 || calls[0].Actor != "" || calls[0].Reason != "" {
		t.Errorf("CreateUser calls = %+v", calls)
	}
}

func TestOperatorEmail(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"SUDO_USER": "root-kim", "USER": "root"}, "operator:root-kim"},
		{map[string]string{"USER": "kim"}, "operator:kim"},
		{nil, "operator:unknown"},
	} {
		if got := operatorEmail(func(k string) string { return tt.env[k] }); got != tt.want {
			t.Errorf("operatorEmail(%v) = %q, want %q", tt.env, got, tt.want)
		}
	}
}

func TestUserCommandReasonFlag(t *testing.T) {
	t.Parallel()
	a := &Auth{roles: DefaultRoles()}
	err := runUserCommand(t.Context(), a, []string{"set-role", "--email", "a@example.com", "--reason"}, strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "invalid flags") {
		t.Fatalf("a --reason without a value = %v", err)
	}
	if !strings.Contains(userCommandUsage, "--reason TEXT") {
		t.Error("the usage does not name --reason")
	}
}

func TestEventsPasswordReset(t *testing.T) {
	t.Parallel()
	capture := &mail.Capture{}
	f := resetFixture(t, capture)
	rec := &recorder{}
	f.auth.events = rec.sink
	f.register(t, "reset@example.com")
	rec.take()
	if r := f.do(t, request{method: http.MethodPost, path: "/api/auth/reset/request", body: map[string]string{"email": "reset@example.com"}}); r.Code != http.StatusAccepted {
		t.Fatalf("request: %d", r.Code)
	}
	token := waitResetToken(t, capture)
	if r := f.do(t, request{method: http.MethodPost, path: "/api/auth/reset/confirm", body: map[string]string{"token": token, "password": "a new password 1"}}); r.Code != http.StatusNoContent {
		t.Fatalf("confirm: %d %s", r.Code, r.Body)
	}
	calls := rec.take()
	if names(calls) != "password_reset" || !calls[0].inTx || calls[0].User.Email != "reset@example.com" || calls[0].User.ID == 0 || calls[0].User.Role != "staff" {
		t.Fatalf("calls = %+v", calls)
	}
}

// TestEventsOIDCWithAuditTrail runs the OIDC account code on the auth pack schema
// with the audit migration: the identity insert writes one event with a composite
// record ID, and the trigger never stops the registration (ADR 0025 decision 4.6).
func TestEventsOIDCWithAuditTrail(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	f := oidcDBFixture(t, WithEvents(rec.sink))
	sql, err := os.ReadFile("../../internal/templates/packs/audit/files/api/db/migrations/00006_audit_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(sql), "-- +goose Down")
	if _, err := f.pool.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	user, err := f.auth.oidcUser(t.Context(), "google", "1234567", "new@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	f.register(t, "linked@example.com")
	rec.take()
	if _, err := f.auth.oidcUser(t.Context(), "github", "42", "linked@example.com", true); err != nil {
		t.Fatal(err)
	}
	calls := rec.take()
	if names(calls) != "register" || calls[0].User.ID == 0 || calls[0].Method != "oidc" {
		t.Fatalf("link calls = %+v", calls)
	}
	rows, err := f.pool.Query(t.Context(), `SELECT action, table_name, record_id, actor_kind, coalesce(reason, ''), changes::text FROM audit_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	type ev struct{ action, table, record, kind, reason, changes string }
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ev, error) {
		var e ev
		err := r.Scan(&e.action, &e.table, &e.record, &e.kind, &e.reason, &e.changes)
		return e, err
	})
	if err != nil {
		t.Fatal(err)
	}
	var identities []ev
	for _, e := range got {
		if e.table == "user_identities" {
			identities = append(identities, e)
		}
	}
	if len(identities) != 2 || identities[0].record != "google:1234567" || identities[1].record != "github:42" ||
		identities[0].action != "create" || identities[0].kind != "system" || identities[0].reason != "register" {
		t.Fatalf("identity events = %+v (all %+v)", identities, got)
	}
	var changes map[string]map[string]any
	if err := json.Unmarshal([]byte(identities[0].changes), &changes); err != nil || changes["user_id"]["new"] != float64(user.ID) {
		t.Errorf("identity changes = %s, %v", identities[0].changes, err)
	}
	// The link removes the password of the existing account: one users update, redacted.
	var linkUpdate bool
	for _, e := range got {
		if e.table == "users" && e.action == "update" && strings.Contains(e.changes, `"password_hash": {"redacted": true}`) {
			linkUpdate = true
		}
	}
	if !linkUpdate {
		t.Errorf("no redacted users update for the link: %+v", got)
	}
}

func TestEventsOIDCSignIn(t *testing.T) {
	t.Parallel()
	p := newOIDCTestProvider(t)
	app := newOIDCTestApp(t, p)
	rec := &recorder{}
	app.h.auth.events = rec.sink
	cookie, callback := app.start(t, "/account")
	if r := app.callback(t, cookie, callback); r.Code != http.StatusSeeOther {
		t.Fatalf("callback: %d %s", r.Code, r.Body)
	}
	if calls := rec.take(); names(calls) != "sign_in" || calls[0].Method != "oidc" || calls[0].User.ID != 42 {
		t.Fatalf("calls = %+v", calls)
	}
}
