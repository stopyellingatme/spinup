package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/mail"
)

// within runs f and fails the test when f does not return within d.
func within(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %s", what, d)
	}
}

// H1-2: a login that passed the password check before a reset must not
// survive the reset, also when its session commit comes after the reset.
func TestLoginRacingResetLosesSession(t *testing.T) {
	t.Parallel()
	var armed atomic.Bool
	reached := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	// The sign_in event comes after the password check and before the session
	// commit, so the barrier holds the login between the two.
	sink := func(_ context.Context, _ Execer, e Event) error {
		if e.Name == EventSignIn && armed.CompareAndSwap(true, false) {
			close(reached)
			<-release
		}
		return nil
	}
	capture := &mail.Capture{}
	f := resetFixture(t, capture, WithEvents(sink))
	old := f.register(t, "race@example.com")
	if rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/reset/request", body: map[string]string{"email": "race@example.com"}}); rec.Code != http.StatusAccepted {
		t.Fatalf("reset request: %d", rec.Code)
	}
	token := waitResetToken(t, capture)

	armed.Store(true)
	logins := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		logins <- f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("race@example.com", goodPassword)})
	}()
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the login did not reach the barrier")
	}
	if rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/reset/confirm", body: map[string]string{"token": token, "password": "new correct password"}}); rec.Code != http.StatusNoContent {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	unblock()
	login := <-logins
	if login.Code != http.StatusOK || sessionCookie(login) == nil {
		t.Fatalf("raced login: %d %s", login.Code, login.Body)
	}
	for name, cookie := range map[string]string{"old": old, "raced": sessionCookie(login).Value} {
		if rec := f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: cookie}); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s session survived the reset: %d", name, rec.Code)
		}
	}
	fresh := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("race@example.com", "new correct password")})
	if fresh.Code != http.StatusOK {
		t.Fatalf("login with the new password: %d", fresh.Code)
	}
	if rec := f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: sessionCookie(fresh).Value}); rec.Code != http.StatusOK {
		t.Fatalf("new session: %d", rec.Code)
	}
}

// insertUnrelatedSessions inserts n sessions of a user that does not exist, plus
// one row whose data no codec can decode.
func insertUnrelatedSessions(t *testing.T, f *fixture, n int) {
	t.Helper()
	data, err := f.auth.sessions.Codec.Encode(time.Now().Add(time.Hour), map[string]any{sessionUserKey: int64(987654321)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO sessions (token, data, expiry) SELECT 'unrelated-' || g, $1, now() + interval '1 hour' FROM generate_series(1, $2::int) g`, data, n); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO sessions (token, data, expiry) VALUES ('malformed', '\x006e6f7420676f62'::bytea, now() + interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
}

func countSessions(t *testing.T, f *fixture, like string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM sessions WHERE token LIKE $1`, like).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// H1-7: a reset reads no unrelated session, so a malformed one cannot stop it.
func TestResetSkipsUnrelatedSessions(t *testing.T) {
	t.Parallel()
	capture := &mail.Capture{}
	f := resetFixture(t, capture)
	first := f.register(t, "target@example.com")
	second := sessionCookie(f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("target@example.com", goodPassword)})).Value
	other := f.register(t, "other@example.com")
	insertUnrelatedSessions(t, f, 500)

	f.do(t, request{method: http.MethodPost, path: "/api/auth/reset/request", body: map[string]string{"email": "target@example.com"}})
	token := waitResetToken(t, capture)
	if rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/reset/confirm", body: map[string]string{"token": token, "password": "new correct password"}}); rec.Code != http.StatusNoContent {
		t.Fatalf("confirm with unrelated sessions: %d %s", rec.Code, rec.Body)
	}
	for _, cookie := range []string{first, second} {
		if rec := f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: cookie}); rec.Code != http.StatusUnauthorized {
			t.Fatalf("a session of the reset user survived: %d", rec.Code)
		}
	}
	if rec := f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: other}); rec.Code != http.StatusOK {
		t.Fatalf("the session of another user ended: %d", rec.Code)
	}
	if n := countSessions(t, f, "unrelated-%") + countSessions(t, f, "malformed"); n != 501 {
		t.Fatalf("unrelated sessions = %d, want 501", n)
	}
	var attempts int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM login_attempts WHERE email = 'target@example.com'`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("login attempts after reset = %d (%v), want 0", attempts, err)
	}
}

// H1-7: an OIDC link reads no unrelated session either.
func TestOIDCLinkSkipsUnrelatedSessions(t *testing.T) {
	t.Parallel()
	f := oidcDBFixture(t)
	old := f.register(t, "person@example.com")
	other := f.register(t, "other@example.com")
	insertUnrelatedSessions(t, f, 100)
	user, version, err := f.auth.oidcUser(t.Context(), "test", "subject-1", "person@example.com", true)
	if err != nil {
		t.Fatalf("link with a malformed unrelated session: %v", err)
	}
	if version != 2 {
		t.Fatalf("auth version after the link = %d, want 2", version)
	}
	if rec := f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: old}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the session before the link survived: %d", rec.Code)
	}
	if rec := f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: other}); rec.Code != http.StatusOK {
		t.Fatalf("the session of another user ended: %d", rec.Code)
	}
	// A known identity signs in again with the current version.
	again, againVersion, err := f.auth.oidcUser(t.Context(), "test", "subject-1", "person@example.com", true)
	if err != nil || again.ID != user.ID || againVersion != 2 {
		t.Fatalf("known identity: %+v version %d %v", again, againVersion, err)
	}
}

// H1-5: an escaped path that the mux routes to a limited handler uses the
// budget of the canonical path.
func TestRateLimitEncodedPaths(t *testing.T) {
	t.Parallel()
	_, h, _ := limitApp(t, WithRateLimits(RateLimits{
		OIDCCallback: Limit{Burst: 2, Every: time.Hour},
		Login:        Limit{Burst: 1, Every: time.Hour},
	}))
	const remote = "203.0.113.7:1234"
	const canonical, encoded = "/api/auth/oidc/demo/callback", "/%61pi/auth/oidc/demo/callback"
	for i, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, canonical, http.StatusNoContent},
		{http.MethodGet, encoded, http.StatusNoContent}, // the mux serves it: the budget is now used
		// A limited callback redirects the browser to the login page.
		{http.MethodGet, canonical, http.StatusSeeOther},
		{http.MethodGet, encoded, http.StatusSeeOther},
		{http.MethodGet, "/api/auth/oidc/%64emo/callback", http.StatusSeeOther},
		{http.MethodPost, "/api/auth/%6Cogin", http.StatusNoContent},
		{http.MethodPost, "/%61pi/auth/login", http.StatusTooManyRequests},
		{http.MethodPost, pathLogin, http.StatusTooManyRequests},
	} {
		if rec := send(t, h, tc.method, tc.path, remote); rec.Code != tc.status {
			t.Errorf("request %d %s %s: status %d, want %d", i, tc.method, tc.path, rec.Code, tc.status)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// H1-8: a caller stops waiting for discovery when its context ends, callers
// share one discovery, and a failure stays cached for oidcRetryAfter.
func TestOIDCDiscoveryCancelShareAndRetry(t *testing.T) {
	t.Parallel()
	c := &clock{now: t0}
	var calls atomic.Int32
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	h := &oidcHandler{auth: &Auth{now: c.Now}, client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return nil, errors.New("provider unavailable")
	})}}
	p := &oidcProvider{issuer: "https://provider.invalid"}

	first := make(chan error, 1)
	go func() {
		_, err := h.discover(context.Background(), p)
		first <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery did not start")
	}
	within(t, time.Second, "a canceled caller", func() {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := h.discover(ctx, p); !errors.Is(err, context.Canceled) {
			t.Errorf("canceled caller: %v, want context.Canceled", err)
		}
	})
	within(t, time.Second, "a caller with a short deadline", func() {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		if _, err := h.discover(ctx, p); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("caller with a deadline: %v, want context.DeadlineExceeded", err)
		}
	})
	second := make(chan error, 1)
	go func() {
		_, err := h.discover(context.Background(), p)
		second <- err
	}()
	unblock()
	for name, ch := range map[string]chan error{"first": first, "second": second} {
		select {
		case err := <-ch:
			if err == nil {
				t.Errorf("%s caller: no error from a failed discovery", name)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s caller did not return", name)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("discovery requests = %d, want 1 shared request", n)
	}
	within(t, time.Second, "a caller after a failure", func() {
		if _, err := h.discover(context.Background(), p); err == nil {
			t.Error("cached failure: no error")
		}
	})
	if n := calls.Load(); n != 1 {
		t.Fatalf("discovery requests after a cached failure = %d, want 1", n)
	}
	c.Add(oidcRetryAfter)
	if _, err := h.discover(context.Background(), p); err == nil {
		t.Fatal("retry: no error")
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("discovery requests after oidcRetryAfter = %d, want 2", n)
	}
}

// H1-11: cleanup deletes reset rows that were used or expired more than a day
// ago, and works when the password_resets table does not exist.
func TestCleanupDeletesOldResets(t *testing.T) {
	t.Parallel()
	t.Run("table", func(t *testing.T) {
		t.Parallel()
		f := resetFixture(t, &mail.Capture{})
		f.register(t, "cleanup@example.com")
		var id int64
		if err := f.pool.QueryRow(t.Context(), `SELECT id FROM users WHERE email = 'cleanup@example.com'`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		now := f.clock.Now()
		ago := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
		rows := []struct {
			name    string
			expires time.Time
			used    *time.Time
			keep    bool
		}{
			{"used long ago", now.Add(-23 * time.Hour), ago(25 * time.Hour), false},
			{"expired long ago", now.Add(-25 * time.Hour), nil, false},
			{"valid", now.Add(time.Hour), nil, true},
			{"used recently", now.Add(30 * time.Minute), ago(time.Hour), true},
			{"expired recently", now.Add(-time.Hour), nil, true},
		}
		for _, row := range rows {
			digest := sha256.Sum256([]byte(row.name))
			if _, err := f.pool.Exec(t.Context(), `INSERT INTO password_resets (token_hash, user_id, expires_at, used_at) VALUES ($1, $2, $3, $4)`, digest[:], id, row.expires, row.used); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.auth.deleteExpired(t.Context()); err != nil {
			t.Fatal(err)
		}
		left, err := f.pool.Query(t.Context(), `SELECT token_hash FROM password_resets`)
		if err != nil {
			t.Fatal(err)
		}
		hashes, err := pgx.CollectRows(left, pgx.RowTo[[]byte])
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			digest := sha256.Sum256([]byte(row.name))
			kept := slices.ContainsFunc(hashes, func(h []byte) bool { return string(h) == string(digest[:]) })
			if kept != row.keep {
				t.Errorf("%s: kept = %v, want %v", row.name, kept, row.keep)
			}
		}
	})
	t.Run("no table", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		if err := f.auth.deleteExpired(t.Context()); err != nil {
			t.Fatalf("cleanup without password_resets: %v", err)
		}
	})
}

// The constant dummy hash must keep the cost of a real check.
func TestDefaultDummyHash(t *testing.T) {
	t.Parallel()
	p, _, _, err := parseHash(defaultDummyHash)
	if err != nil || p != defaultParams {
		t.Fatalf("defaultDummyHash has %+v (%v), want defaultParams %+v.\nTry: replace it with %s", p, err, defaultParams, hashPassword(defaultParams, dummyPassword))
	}
	if ok, _, err := verifyPassword(defaultDummyHash, dummyPassword); err != nil || !ok {
		t.Fatalf("defaultDummyHash does not verify dummyPassword: %v %v", ok, err)
	}
	if got := dummyHash(defaultParams)(); got != defaultDummyHash {
		t.Fatalf("dummyHash(defaultParams) = %q, want the constant", got)
	}
	lazy := dummyHash(testParams)
	once := lazy()
	if p, _, _, err := parseHash(once); err != nil || p != testParams || lazy() != once {
		t.Fatalf("custom dummy hash: %+v %v, stable %v", p, err, lazy() == once)
	}
	// New computes no hash with the default parameters.
	a, err := New(lazyPool(t), withEnv(nil), func(o *options) { o.cleanup = 0 })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close(context.Background()) }()
	if a.dummy() != defaultDummyHash {
		t.Fatal("New with defaultParams does not use the constant dummy hash")
	}
}

// A login for an unknown email runs one full verification, as a known email does.
func TestUnknownEmailVerifiesOneHash(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.register(t, "known@example.com")
	var mu sync.Mutex
	var seen []string
	f.auth.verify = func(encoded, password string) (bool, params, error) {
		mu.Lock()
		seen = append(seen, encoded)
		mu.Unlock()
		return verifyPassword(encoded, password)
	}
	for _, email := range []string{"unknown@example.com", "known@example.com"} {
		if rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds(email, "wrong password!")}); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status %d", email, rec.Code)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("verifications = %d, want one for each login", len(seen))
	}
	if seen[0] != f.auth.dummy() {
		t.Fatal("the unknown email did not verify the dummy hash")
	}
	for i, encoded := range seen {
		if p, _, _, err := parseHash(encoded); err != nil || p != f.auth.params {
			t.Fatalf("verification %d used %+v (%v), want the current parameters", i, p, err)
		}
	}
}

// queryCounter counts the queries of a pool.
type queryCounter struct{ n atomic.Int32 }

func (q *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	q.n.Add(1)
	return ctx
}

func (q *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// The session store reads the session and its user in one query.
func TestSessionUserOneQuery(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	cookie := f.register(t, "one@example.com")
	var stored *int64
	if err := f.pool.QueryRow(t.Context(), `SELECT user_id FROM sessions`).Scan(&stored); err != nil || stored == nil {
		t.Fatalf("sessions.user_id after login = %v (%v), want the user", stored, err)
	}
	counter := &queryCounter{}
	cfg := f.pool.Config()
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f.auth.pool = pool
	if rec := f.do(t, request{method: http.MethodGet, path: "/api/private", cookie: cookie}); rec.Code != http.StatusOK || rec.Body.String() != "hello one@example.com" {
		t.Fatalf("private: %d %s", rec.Code, rec.Body)
	}
	if n := counter.n.Load(); n != 1 {
		t.Fatalf("queries for an authenticated request = %d, want 1", n)
	}
	// A session from before migration 00007 has no user_id. It still works.
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET user_id = NULL`); err != nil {
		t.Fatal(err)
	}
	counter.n.Store(0)
	if rec := f.do(t, request{method: http.MethodGet, path: "/api/private", cookie: cookie}); rec.Code != http.StatusOK {
		t.Fatalf("legacy session: %d %s", rec.Code, rec.Body)
	}
	if n := counter.n.Load(); n != 2 {
		t.Fatalf("queries for a legacy session = %d, want 2", n)
	}
	// A legacy session after a version change ends.
	if _, err := f.pool.Exec(t.Context(), `UPDATE users SET auth_version = auth_version + 1`); err != nil {
		t.Fatal(err)
	}
	if rec := f.do(t, request{method: http.MethodGet, path: "/api/private", cookie: cookie}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("legacy session after a version change: %d", rec.Code)
	}
}

func TestSchemaHint(t *testing.T) {
	t.Parallel()
	err := schemaHint(&pgconn.PgError{Code: "42703", Message: `column s.user_id does not exist`})
	if !strings.Contains(err.Error(), "00007_users_auth_version.sql") || !strings.Contains(err.Error(), "\nTry: run spinup db migrate up") {
		t.Fatalf("hint: %v", err)
	}
	other := errors.New("other")
	if got := schemaHint(other); got == nil || got.Error() != "other" || schemaHint(nil) != nil {
		t.Fatal("schemaHint changed an unrelated error")
	}
}
