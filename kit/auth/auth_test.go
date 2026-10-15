package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/config"
	"github.com/stopyellingatme/spinup/kit/httpx"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

// testParams keep the tests fast. TestPasswordDefaultParams covers the defaults.
var testParams = params{memory: 64, time: 1, threads: 1, saltLen: 16, keyLen: 32}

const goodPassword = "correct horse battery"

// clock is a settable time source for lockout tests.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fixture struct {
	auth    *Auth
	pool    *pgxpool.Pool
	handler http.Handler
	clock   *clock
}

func newFixture(t *testing.T, opts ...Option) *fixture {
	t.Helper()
	pool := testkit.DBWithMigrations(t, os.DirFS("testdata/migrations"))
	c := &clock{now: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	base := []Option{WithSecureCookie(false), func(o *options) { o.params = testParams; o.now = c.Now }}
	a, err := New(pool, append(base, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	mux := http.NewServeMux()
	a.Routes(mux)
	mux.Handle("GET /api/private", a.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ := UserFrom(r.Context())
		_, _ = io.WriteString(w, "hello "+user.Email)
	})))
	mux.HandleFunc("POST /api/things", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })
	mux.HandleFunc("GET /api/framed", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'")
	})
	return &fixture{auth: a, pool: pool, handler: httpx.Chain(mux, httpx.RequestID(), a.Middleware()), clock: c}
}

// request describes one call to the test handler.
type request struct {
	method, path string
	body         any // a string is sent as is; other values are JSON-encoded
	cookie       string
	headers      map[string]string
}

func (f *fixture) do(t *testing.T, req request) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	switch v := req.body.(type) {
	case nil:
	case string:
		body = strings.NewReader(v)
	default:
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		body = strings.NewReader(string(data))
	}
	r := httptest.NewRequestWithContext(t.Context(), req.method, "http://api.test"+req.path, body)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if req.cookie != "" {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: req.cookie})
	}
	for k, v := range req.headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, r)
	return rec
}

func creds(email, password string) map[string]string {
	return map[string]string{"email": email, "password": password}
}

// sessionCookie returns the session cookie of rec, or nil.
func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	return nil
}

func (f *fixture) register(t *testing.T, email string) string {
	t.Helper()
	rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/register", body: creds(email, goodPassword)})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register %s: status %d: %s", email, rec.Code, rec.Body)
	}
	c := sessionCookie(rec)
	if c == nil || c.Value == "" {
		t.Fatalf("register %s: no session cookie", email)
	}
	return c.Value
}

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json (status %d, body %s)", ct, rec.Code, rec.Body)
	}
	return testkit.DecodeJSON[httpx.Problem](t, rec)
}

func fieldNames(p httpx.Problem) string {
	var names []string
	for _, e := range p.Errors {
		names = append(names, e.Field)
	}
	return strings.Join(names, ",")
}

func TestRegisterSignsIn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/register", body: creds("  Alice@Example.COM ", goodPassword)})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := testkit.DecodeJSON[userResponse](t, rec)
	if got.User.Email != "alice@example.com" || got.User.ID <= 0 {
		t.Fatalf("user = %+v, want the normalized email and an ID", got.User)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	c := sessionCookie(rec)
	if c == nil {
		t.Fatal("no session cookie")
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Secure || c.MaxAge <= 0 {
		t.Errorf("cookie = %+v, want HttpOnly, SameSite=Lax, Path=/, not Secure, persistent", c)
	}

	// The store holds a hash of the token, never the token.
	var stored int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM sessions WHERE token = $1`, c.Value).Scan(&stored); err != nil || stored != 0 {
		t.Errorf("sessions rows with the raw token = %d (%v), want 0", stored, err)
	}

	rec = f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: c.Value})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/session: status %d: %s", rec.Code, rec.Body)
	}
	if s := testkit.DecodeJSON[userResponse](t, rec); s.User != got.User {
		t.Errorf("session user = %+v, want %+v", s.User, got.User)
	}
	var hash string
	if err := f.pool.QueryRow(t.Context(), `SELECT password_hash FROM users WHERE id = $1`, got.User.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$") || strings.Contains(hash, goodPassword) {
		t.Errorf("password_hash = %q, want an argon2id PHC string", hash)
	}
}

func TestSecureCookieByDefault(t *testing.T) {
	t.Parallel()
	f := newFixture(t, WithSecureCookie(true))
	rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/register", body: creds("secure@example.com", goodPassword)})
	if c := sessionCookie(rec); c == nil || !c.Secure {
		t.Fatalf("cookie = %+v, want Secure", c)
	}
}

func TestCookieSecurityFromEnvironment(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, env string
		secure    bool
	}{
		{"unset", "", true},
		{"development", "dev", false},
		{"production", "production", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var cfg config.Base
			if err := config.LoadFrom(&cfg, func(key string) (string, bool) {
				return tc.env, key == "SPINUP_ENV" && tc.env != ""
			}, os.ReadFile); err != nil {
				t.Fatal(err)
			}
			// New does not connect until a request needs the store.
			pool, err := pgxpool.New(t.Context(), "postgres://localhost/unused")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			a, err := New(pool, WithSecureCookie(cfg.Env != "dev"), func(o *options) {
				o.params, o.cleanup = testParams, 0
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := a.Close(t.Context()); err != nil {
					t.Error(err)
				}
			}()
			if got := a.sessions.Cookie.Secure; got != tc.secure {
				t.Fatalf("Secure = %v, want %v (Env = %q)", got, tc.secure, cfg.Env)
			}
		})
	}
}

func TestRegisterValidation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, tc := range []struct {
		name   string
		body   any
		ctype  string
		status int
		fields string
	}{
		{"empty object", map[string]string{}, "", http.StatusUnprocessableEntity, "email,password"},
		{"bad email", creds("not-an-email", goodPassword), "", http.StatusUnprocessableEntity, "email"},
		{"display name", creds("Alice <alice@example.com>", goodPassword), "", http.StatusUnprocessableEntity, "email"},
		{"long email", creds(strings.Repeat("a", 250)+"@x.io", goodPassword), "", http.StatusUnprocessableEntity, "email"},
		{"short password", creds("a@example.com", strings.Repeat("p", MinPasswordLength-1)), "", http.StatusUnprocessableEntity, "password"},
		{"long password", creds("a@example.com", strings.Repeat("p", MaxPasswordLength+1)), "", http.StatusUnprocessableEntity, "password"},
		{"unknown field", map[string]string{"email": "a@example.com", "password": goodPassword, "admin": "true"}, "", http.StatusBadRequest, ""},
		{"not JSON", "email=a@example.com", "", http.StatusBadRequest, ""},
		{"form body", "email=a@example.com", "application/x-www-form-urlencoded", http.StatusUnsupportedMediaType, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := request{method: http.MethodPost, path: "/api/auth/register", body: tc.body}
			if tc.ctype != "" {
				req.headers = map[string]string{"Content-Type": tc.ctype}
			}
			rec := f.do(t, req)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if got := fieldNames(problemOf(t, rec)); got != tc.fields {
				t.Errorf("field errors = %q, want %q", got, tc.fields)
			}
			if sessionCookie(rec) != nil {
				t.Error("a failed registration set a session cookie")
			}
		})
	}
	// Exactly the limits pass, counted in characters, not bytes.
	if rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/register", body: creds("min@example.com", strings.Repeat("é", MinPasswordLength))}); rec.Code != http.StatusCreated {
		t.Errorf("12-character password: status %d: %s", rec.Code, rec.Body)
	}
	if rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/register", body: creds("max@example.com", strings.Repeat("p", MaxPasswordLength))}); rec.Code != http.StatusCreated {
		t.Errorf("512-character password: status %d: %s", rec.Code, rec.Body)
	}
}

func TestRegisterDuplicate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.register(t, "bob@example.com")
	rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/register", body: creds(" BOB@example.com", "another long password")})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", rec.Code, rec.Body)
	}
	if got := fieldNames(problemOf(t, rec)); got != "email" {
		t.Errorf("field errors = %q, want email", got)
	}
	if sessionCookie(rec) != nil {
		t.Error("a duplicate registration set a session cookie")
	}
}

func TestLoginRotatesToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	first := f.register(t, "carol@example.com")
	rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("Carol@Example.com", goodPassword), cookie: first})
	if rec.Code != http.StatusOK {
		t.Fatalf("login: status %d: %s", rec.Code, rec.Body)
	}
	if got := testkit.DecodeJSON[userResponse](t, rec); got.User.Email != "carol@example.com" {
		t.Errorf("user = %+v", got.User)
	}
	c := sessionCookie(rec)
	if c == nil || c.Value == "" || c.Value == first {
		t.Fatalf("login cookie = %+v, want a new token", c)
	}
	if rec := f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: first}); rec.Code != http.StatusUnauthorized {
		t.Errorf("old token: status %d, want 401", rec.Code)
	}
	if rec := f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: c.Value}); rec.Code != http.StatusOK {
		t.Errorf("new token: status %d, want 200", rec.Code)
	}
	// A cookie that the server never issued gets no session (no fixation).
	rec = f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("carol@example.com", goodPassword), cookie: "attacker-chosen"})
	if c := sessionCookie(rec); c == nil || c.Value == "attacker-chosen" {
		t.Errorf("login with a foreign cookie: cookie = %+v, want a new token", c)
	}
}

func TestLoginFailures(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.register(t, "dave@example.com")
	wrong := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("dave@example.com", "wrong password!")})
	unknown := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("nobody@example.com", "wrong password!")})
	for name, rec := range map[string]*httptest.ResponseRecorder{"wrong password": wrong, "unknown email": unknown} {
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, rec.Code)
		}
		if p := problemOf(t, rec); p.Detail != "The email address or password is incorrect." {
			t.Errorf("%s: detail %q", name, p.Detail)
		}
		if sessionCookie(rec) != nil {
			t.Errorf("%s: set a session cookie", name)
		}
	}
	rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("dave@example.com", "")})
	if rec.Code != http.StatusUnprocessableEntity || fieldNames(problemOf(t, rec)) != "password" {
		t.Errorf("empty password: status %d: %s", rec.Code, rec.Body)
	}
}

func TestLockoutAndRecovery(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.register(t, "erin@example.com")
	login := func(password string) *httptest.ResponseRecorder {
		return f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("erin@example.com", password)})
	}
	for i := range DefaultLockoutThreshold {
		if rec := login("wrong password!"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: status %d, want 401", i+1, rec.Code)
		}
		f.clock.Add(time.Minute)
	}
	// The sixth attempt is locked, even with the right password.
	rec := login(goodPassword)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked login: status %d, want 429: %s", rec.Code, rec.Body)
	}
	problemOf(t, rec)
	// The first failure was at 12:00; the window opens at 12:15; now is 12:05.
	if got := rec.Header().Get("Retry-After"); got != strconv.Itoa(10*60) {
		t.Errorf("Retry-After = %q, want 600", got)
	}
	// Other emails are not locked.
	if rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("other@example.com", "wrong password!")}); rec.Code != http.StatusUnauthorized {
		t.Errorf("other email: status %d, want 401", rec.Code)
	}

	f.clock.Add(10*time.Minute + time.Second)
	if rec := login(goodPassword); rec.Code != http.StatusOK {
		t.Fatalf("login after the window: status %d: %s", rec.Code, rec.Body)
	}
	var left int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM login_attempts WHERE email = 'erin@example.com'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("login_attempts after success = %d (%v), want 0", left, err)
	}
	// A success cleared the count: four more failures do not lock.
	for i := range DefaultLockoutThreshold - 1 {
		if rec := login("wrong password!"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d after success: status %d, want 401", i+1, rec.Code)
		}
	}
	if rec := login(goodPassword); rec.Code != http.StatusOK {
		t.Fatalf("login after four failures: status %d", rec.Code)
	}
}

func TestLockoutOption(t *testing.T) {
	t.Parallel()
	f := newFixture(t, WithLockout(2, time.Hour))
	for range 2 {
		f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("frank@example.com", "wrong password!")})
	}
	rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("frank@example.com", "wrong password!")})
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "3600" {
		t.Fatalf("status %d, Retry-After %q, want 429 and 3600", rec.Code, rec.Header().Get("Retry-After"))
	}
	// Cleanup deletes the attempts that left the window.
	f.clock.Add(2 * time.Hour)
	if err := f.auth.deleteExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM login_attempts`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("login_attempts after cleanup = %d (%v), want 0", left, err)
	}
}

// loginCheckTracer signals when a login reaches the password lookup.
// Busy hash slots then keep password verification from finishing.
type loginCheckTracer struct{ checked chan struct{} }
type loginCheckKey struct{}

// TraceQueryStart marks credential queries for TraceQueryEnd.
func (tr *loginCheckTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, loginCheckKey{}, strings.HasPrefix(data.SQL, "SELECT id, email, password_hash FROM users"))
}

// TraceQueryEnd reports a completed credential query.
func (tr *loginCheckTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if checked, _ := ctx.Value(loginCheckKey{}).(bool); checked {
		tr.checked <- struct{}{}
	}
}

func TestConcurrentLoginLockout(t *testing.T) {
	t.Parallel()
	for _, known := range []bool{true, false} {
		t.Run(strconv.FormatBool(known), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			if known {
				f.register(t, "burst@example.com")
			}
			const attempts = 20
			tracer := &loginCheckTracer{checked: make(chan struct{}, attempts)}
			cfg := f.pool.Config()
			cfg.ConnConfig.Tracer = tracer
			pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			f.auth.pool = pool
			// Hold every hash slot until all requests are admitted or refused.
			for range cap(f.auth.slots) {
				f.auth.slots <- struct{}{}
			}
			var release sync.Once
			releaseSlots := func() {
				release.Do(func() {
					for range cap(f.auth.slots) {
						<-f.auth.slots
					}
				})
			}
			results := make(chan *httptest.ResponseRecorder, attempts)
			var wg sync.WaitGroup
			defer wg.Wait()
			// Release the slots before waiting, including on a test failure.
			defer releaseSlots()
			start := make(chan struct{})
			for i := range attempts {
				wg.Go(func() {
					<-start
					email := "burst@example.com"
					if i%2 == 0 {
						email = "  BURST@Example.COM  "
					}
					results <- f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds(email, "wrong password!")})
				})
			}
			close(start)
			timeout := time.NewTimer(10 * time.Second)
			defer timeout.Stop()
			checks, locked := 0, 0
			for range attempts {
				select {
				case <-tracer.checked:
					checks++
				case rec := <-results:
					if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "900" {
						t.Fatalf("blocked login: status %d, Retry-After %q: %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
					}
					locked++
				case <-timeout.C:
					t.Fatal("logins did not reach the password check or return 429")
				}
			}
			if checks != DefaultLockoutThreshold || locked != attempts-DefaultLockoutThreshold {
				t.Errorf("password checks = %d, locked = %d, want 5 and 15", checks, locked)
			}
			releaseSlots()
			for range checks {
				if rec := <-results; rec.Code != http.StatusUnauthorized {
					t.Errorf("wrong login: status %d: %s", rec.Code, rec.Body)
				}
			}
			var rows int
			if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM login_attempts WHERE email = 'burst@example.com'`).Scan(&rows); err != nil || rows != DefaultLockoutThreshold {
				t.Fatalf("login attempts = %d (%v), want 5", rows, err)
			}
		})
	}
}

func TestLogout(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.register(t, "grace@example.com")
	rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/logout", cookie: token})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: status %d: %s", rec.Code, rec.Body)
	}
	c := sessionCookie(rec)
	if c == nil || c.Value != "" || c.MaxAge >= 0 {
		t.Fatalf("logout cookie = %+v, want an expired empty cookie", c)
	}
	if rec := f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: token}); rec.Code != http.StatusUnauthorized {
		t.Errorf("session after logout: status %d, want 401", rec.Code)
	}
	var rows int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM sessions`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("sessions rows after logout = %d (%v), want 0", rows, err)
	}
	// Logout without a session also succeeds.
	if rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/logout"}); rec.Code != http.StatusNoContent {
		t.Errorf("logout without a session: status %d", rec.Code)
	}
}

func TestSessionAndRequireUser(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	rec := f.do(t, request{method: http.MethodGet, path: "/api/session"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie: status %d, want 401", rec.Code)
	}
	if p := problemOf(t, rec); p.Status != http.StatusUnauthorized {
		t.Errorf("problem = %+v, want status 401", p)
	}
	if rec := f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: "unknown-token"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("unknown token: status %d, want 401", rec.Code)
	}
	if rec := f.do(t, request{method: http.MethodGet, path: "/api/private"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("RequireUser without a user: status %d, want 401", rec.Code)
	}
	token := f.register(t, "heidi@example.com")
	rec = f.do(t, request{method: http.MethodGet, path: "/api/private", cookie: token})
	if rec.Code != http.StatusOK || rec.Body.String() != "hello heidi@example.com" {
		t.Errorf("RequireUser with a user: status %d, body %q", rec.Code, rec.Body)
	}
	// A deleted user loses the session.
	if _, err := f.pool.Exec(t.Context(), `DELETE FROM users WHERE email = 'heidi@example.com'`); err != nil {
		t.Fatal(err)
	}
	rec = f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: token})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("deleted user: status %d, want 401", rec.Code)
	}
	if c := sessionCookie(rec); c == nil || c.MaxAge >= 0 {
		t.Errorf("deleted user: cookie = %+v, want an expired cookie", c)
	}
}

func TestCrossOriginProtection(t *testing.T) {
	t.Parallel()
	f := newFixture(t, WithTrustedOrigins("http://localhost:5173", ""))
	for _, tc := range []struct {
		name    string
		method  string
		headers map[string]string
		want    int
	}{
		{"cross-site fetch", http.MethodPost, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"same-site fetch", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"foreign origin", http.MethodPost, map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"null origin", http.MethodPost, map[string]string{"Origin": "null"}, http.StatusForbidden},
		{"cross-site with trusted origin", http.MethodPost, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "http://localhost:5173"}, http.StatusCreated},
		{"trusted origin (SvelteKit server)", http.MethodPost, map[string]string{"Origin": "http://localhost:5173"}, http.StatusCreated},
		{"same origin", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://api.test"}, http.StatusCreated},
		{"origin equals host", http.MethodPost, map[string]string{"Origin": "http://api.test"}, http.StatusCreated},
		{"no browser headers", http.MethodPost, nil, http.StatusCreated},
		{"safe method cross-site", http.MethodGet, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := "/api/things"
			if tc.method == http.MethodGet {
				path = "/api/session"
			}
			rec := f.do(t, request{method: tc.method, path: path, headers: tc.headers})
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			if tc.want == http.StatusForbidden {
				if p := problemOf(t, rec); p.Status != http.StatusForbidden {
					t.Errorf("problem status %d", p.Status)
				}
			}
		})
	}
	// The login endpoint is protected too.
	rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("a@example.com", goodPassword), headers: map[string]string{"Sec-Fetch-Site": "cross-site"}})
	if rec.Code != http.StatusForbidden || sessionCookie(rec) != nil {
		t.Errorf("cross-site login: status %d", rec.Code)
	}
}

func TestDefaultCSP(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if got := f.do(t, request{method: http.MethodGet, path: "/api/session"}).Header().Get("Content-Security-Policy"); got != DefaultCSP {
		t.Errorf("CSP = %q, want %q", got, DefaultCSP)
	}
	if got := f.do(t, request{method: http.MethodGet, path: "/api/framed"}).Header().Get("Content-Security-Policy"); got != "default-src 'self'" {
		t.Errorf("CSP = %q, want the handler's policy", got)
	}
}

func TestRehashOnLogin(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.register(t, "ivan@example.com")
	newer := params{memory: 128, time: 2, threads: 1, saltLen: 16, keyLen: 32}
	f.auth.params = newer
	if rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("ivan@example.com", goodPassword)}); rec.Code != http.StatusOK {
		t.Fatalf("login: status %d", rec.Code)
	}
	var hash string
	if err := f.pool.QueryRow(t.Context(), `SELECT password_hash FROM users WHERE email = 'ivan@example.com'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=128,t=2,p=1$") {
		t.Errorf("hash after login = %q, want the newer parameters", hash)
	}
}

func TestMiddlewareMissing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := http.NewServeMux()
	f.auth.Routes(mux)
	for _, h := range []http.Handler{mux, f.auth.RequireUser(http.NotFoundHandler())} {
		rec := testkit.Do(t, h, http.MethodGet, "/api/session", nil)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status %d, want 500", rec.Code)
		}
	}
}

func TestSessionStoreFailure(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	token := f.register(t, "judy@example.com")
	f.pool.Close()
	rec := f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: token})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500: %s", rec.Code, rec.Body)
	}
	problemOf(t, rec)
}

func TestNewErrors(t *testing.T) {
	t.Parallel()
	pool := testkit.DBWithMigrations(t, os.DirFS("testdata/migrations"))
	for _, tc := range []struct {
		name string
		pool *pgxpool.Pool
		opts []Option
		want string
	}{
		{"nil pool", nil, nil, "pool is nil"},
		{"bad origin", pool, []Option{WithTrustedOrigins("localhost:5173/path")}, "cannot trust origin"},
		{"zero lifetime", pool, []Option{WithLifetime(0)}, "lifetime"},
		{"negative idle timeout", pool, []Option{WithIdleTimeout(-time.Second)}, "idle timeout"},
		{"zero threshold", pool, []Option{WithLockout(0, time.Minute)}, "lockout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := append([]Option{func(o *options) { o.params = testParams }}, tc.opts...)
			a, err := New(tc.pool, opts...)
			if err == nil {
				_ = a.Close(context.Background())
				t.Fatal("New succeeded")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "\nTry: ") {
				t.Errorf("error = %q, want %q and a Try line", err, tc.want)
			}
		})
	}
}

func TestIdleTimeout(t *testing.T) {
	t.Parallel()
	f := newFixture(t, WithIdleTimeout(time.Hour), WithLifetime(24*time.Hour))
	token := f.register(t, "ken@example.com")
	rec := f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: token})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	// With an idle timeout, every request extends the session and sends the cookie.
	c := sessionCookie(rec)
	if c == nil || c.Value != token || c.MaxAge > int(time.Hour/time.Second)+1 {
		t.Errorf("cookie = %+v, want the same token with the idle expiry", c)
	}
}
