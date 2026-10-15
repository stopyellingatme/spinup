package auth

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"golang.org/x/oauth2"
)

func TestOIDCRedirect(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/", true}, {"/account?tab=profile", true}, {"", false},
		{"https://evil.test", false}, {"//evil.test", false}, {"/\\evil.test", false},
		{"/%2f%2fevil.test", false}, {"/%5cevil.test", false}, {"/\nLocation:evil", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			if got := oidcRedirectOK(tc.path); got != tc.want {
				t.Fatalf("redirect %q: %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// The provider serves real discovery, JWKS, authorization, and token endpoints.
// Tests verify signatures through go-oidc rather than bypassing the verifier.
type oidcTestProvider struct {
	server                                          *oidcFakeServer
	key                                             *rsa.PrivateKey
	mu                                              sync.Mutex
	grants                                          map[string]url.Values
	nonce, issuer, audience                         string
	expired, unverified, missingToken, badSignature bool
	discovery                                       atomic.Int32
	tokens                                          atomic.Int32
}

func newOIDCTestProvider(t *testing.T) *oidcTestProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &oidcTestProvider{key: key, grants: map[string]url.Values{}}
	mux := http.NewServeMux()
	p.server = &oidcFakeServer{URL: "http://oidc.test", handler: mux}
	t.Cleanup(p.server.Close)
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		p.discovery.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": p.server.URL, "authorization_endpoint": p.server.URL + "/authorize", "token_endpoint": p.server.URL + "/token", "jwks_uri": p.server.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "test", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" || q.Get("response_type") != "code" {
			http.Error(w, "missing OIDC safeguards", http.StatusBadRequest)
			return
		}
		code := oauth2.GenerateVerifier()
		p.mu.Lock()
		p.grants[code] = q
		p.mu.Unlock()
		callback, _ := url.Parse(q.Get("redirect_uri"))
		callback.RawQuery = url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
		http.Redirect(w, r, callback.String(), http.StatusSeeOther)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		p.tokens.Add(1)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "form", http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		grant, ok := p.grants[r.Form.Get("code")]
		delete(p.grants, r.Form.Get("code"))
		if !ok || oauth2.S256ChallengeFromVerifier(r.Form.Get("code_verifier")) != grant.Get("code_challenge") || r.Form.Get("redirect_uri") != grant.Get("redirect_uri") {
			http.Error(w, "invalid grant", http.StatusBadRequest)
			return
		}
		nonce := grant.Get("nonce")
		if p.nonce != "" {
			nonce = p.nonce
		}
		issuer := p.server.URL
		if p.issuer != "" {
			issuer = p.issuer
		}
		audience := "client"
		if p.audience != "" {
			audience = p.audience
		}
		exp := time.Now().Add(time.Hour).Unix()
		if p.expired {
			exp = time.Now().Add(-time.Hour).Unix()
		}
		claims, _ := json.Marshal(map[string]any{"iss": issuer, "aud": audience, "sub": "subject-1", "exp": exp, "iat": time.Now().Unix(), "nonce": nonce, "email": "person@example.com", "email_verified": !p.unverified})
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test"}`))
		payload := header + "." + base64.RawURLEncoding.EncodeToString(claims)
		digest := sha256.Sum256([]byte(payload))
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			http.Error(w, "sign", http.StatusInternalServerError)
			return
		}
		if p.badSignature {
			sig[0] ^= 1
		}
		result := map[string]any{"access_token": "access", "token_type": "Bearer", "expires_in": 3600}
		if !p.missingToken {
			result["id_token"] = payload + "." + base64.RawURLEncoding.EncodeToString(sig)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
	return p
}

type oidcTestApp struct {
	h       *oidcHandler
	handler http.Handler
	linked  int
	session string
}

func newOIDCTestApp(t *testing.T, p *oidcTestProvider) *oidcTestApp {
	t.Helper()
	a := &Auth{sessions: scs.New(), csrf: http.NewCrossOriginProtection(), logger: slog.Default(), now: time.Now}
	a.sessions.Cookie.Secure = false
	block, err := aes.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	app := &oidcTestApp{}
	h := &oidcHandler{auth: a, origin: "http://app.test", cipher: aead, client: &http.Client{Timeout: time.Second, Transport: p.server}, providers: []*oidcProvider{{name: "test", label: "Test provider", issuer: p.server.URL, clientID: "client", secret: "secret", scopes: []string{"openid", "email", "profile"}}}}
	h.user = func(_ context.Context, provider, subject, email string, verified bool) (User, error) {
		if provider != "test" || subject != "subject-1" || email != "person@example.com" || !verified {
			return User{}, errors.New("wrong identity")
		}
		app.linked++
		return User{ID: 42, Email: email}, nil
	}
	mux := http.NewServeMux()
	mux.Handle("GET /api/auth/oidc/{provider}/start", a.endpoint(h.start))
	mux.Handle("GET /api/auth/oidc/{provider}/callback", a.endpoint(h.callback))
	app.h = h
	app.handler = a.Middleware()(mux)
	return app
}

func (app *oidcTestApp) start(t *testing.T, next string) (*http.Cookie, *url.URL) {
	t.Helper()
	rec := httptest.NewRecorder()
	app.handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://app.test/api/auth/oidc/test/start?next="+url.QueryEscape(next), nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies: %v", cookies)
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != 600 || cookie.Path != "/api/auth/oidc/test/" {
		t.Fatalf("unsafe cookie: %+v", cookie)
	}
	client := *app.h.client
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rec.Header().Get("Location"), nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("authorize: %d", res.StatusCode)
	}
	callback, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return cookie, callback
}

func (app *oidcTestApp) callback(t *testing.T, cookie *http.Cookie, callback *url.URL) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, callback.String(), nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if app.session != "" {
		req.AddCookie(&http.Cookie{Name: CookieName, Value: app.session})
	}
	rec := httptest.NewRecorder()
	app.handler.ServeHTTP(rec, req)
	return rec
}

func TestOIDCFlow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(*oidcTestProvider, *oidcTestApp, *http.Cookie, *url.URL)
		want   int
	}{
		{"success", nil, http.StatusSeeOther},
		{"state mismatch", func(_ *oidcTestProvider, _ *oidcTestApp, _ *http.Cookie, u *url.URL) {
			q := u.Query()
			q.Set("state", "wrong")
			u.RawQuery = q.Encode()
		}, http.StatusBadRequest},
		{"nonce mismatch", func(p *oidcTestProvider, _ *oidcTestApp, _ *http.Cookie, _ *url.URL) { p.nonce = "wrong" }, http.StatusBadRequest},
		{"unverified email", func(p *oidcTestProvider, _ *oidcTestApp, _ *http.Cookie, _ *url.URL) { p.unverified = true }, http.StatusBadRequest},
		{"wrong issuer", func(p *oidcTestProvider, _ *oidcTestApp, _ *http.Cookie, _ *url.URL) { p.issuer = "https://evil.test" }, http.StatusBadRequest},
		{"wrong audience", func(p *oidcTestProvider, _ *oidcTestApp, _ *http.Cookie, _ *url.URL) { p.audience = "another-client" }, http.StatusBadRequest},
		{"expired token", func(p *oidcTestProvider, _ *oidcTestApp, _ *http.Cookie, _ *url.URL) { p.expired = true }, http.StatusBadRequest},
		{"missing ID token", func(p *oidcTestProvider, _ *oidcTestApp, _ *http.Cookie, _ *url.URL) { p.missingToken = true }, http.StatusBadRequest},
		{"invalid signature", func(p *oidcTestProvider, _ *oidcTestApp, _ *http.Cookie, _ *url.URL) { p.badSignature = true }, http.StatusBadRequest},
		{"tampered cookie", func(_ *oidcTestProvider, _ *oidcTestApp, c *http.Cookie, _ *url.URL) { c.Value = "invalid" }, http.StatusBadRequest},
		{"expired cookie", func(_ *oidcTestProvider, a *oidcTestApp, _ *http.Cookie, _ *url.URL) {
			now := a.h.auth.now()
			a.h.auth.now = func() time.Time { return now.Add(oidcLifetime) }
		}, http.StatusBadRequest},
		{"provider error", func(_ *oidcTestProvider, _ *oidcTestApp, _ *http.Cookie, u *url.URL) {
			q := u.Query()
			q.Set("error", "access_denied")
			u.RawQuery = q.Encode()
		}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newOIDCTestProvider(t)
			app := newOIDCTestApp(t, p)
			cookie, callback := app.start(t, "/account?tab=profile")
			if tc.mutate != nil {
				p.mu.Lock()
				tc.mutate(p, app, cookie, callback)
				p.mu.Unlock()
			}
			rec := app.callback(t, cookie, callback)
			if rec.Code != tc.want {
				t.Fatalf("callback: %d %s", rec.Code, rec.Body)
			}
			if tc.want == http.StatusSeeOther {
				if app.linked != 1 || rec.Header().Get("Location") != "/account?tab=profile" || sessionCookie(rec) == nil {
					t.Fatalf("session not started: %v", rec.Header())
				}
				if rec := app.callback(t, cookie, callback); rec.Code != http.StatusBadRequest {
					t.Fatalf("code replay: %d", rec.Code)
				}
			} else if app.linked != 0 || sessionCookie(rec) != nil {
				t.Fatal("invalid response linked an account or started a session")
			}
			if p.discovery.Load() != 1 {
				t.Fatalf("discovery not cached: %d", p.discovery.Load())
			}
		})
	}
}

func TestOIDCOpenRedirectAndProviderDown(t *testing.T) {
	t.Parallel()
	p := newOIDCTestProvider(t)
	app := newOIDCTestApp(t, p)
	for _, path := range []string{"//evil.test", "https://evil.test", "/\\evil.test", "/%2fevil.test"} {
		rec := httptest.NewRecorder()
		app.handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://app.test/api/auth/oidc/test/start?next="+url.QueryEscape(path), nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("redirect %q: %d", path, rec.Code)
		}
	}
	if p.discovery.Load() != 0 {
		t.Fatal("redirect validation performed discovery")
	}
	// Construction does not discover. A down provider fails only its own start.
	p.server.Close()
	rec := httptest.NewRecorder()
	app.handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://app.test/api/auth/oidc/test/start", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "Try:") {
		t.Fatalf("down provider: %d %s", rec.Code, rec.Body)
	}
}

// oidcFakeServer uses real HTTP request and response objects without a socket.
// The lane sandbox forbids listening even on loopback interfaces.
type oidcFakeServer struct {
	URL     string
	handler http.Handler
	closed  atomic.Bool
}

func (s *oidcFakeServer) Close() { s.closed.Store(true) }
func (s *oidcFakeServer) RoundTrip(r *http.Request) (*http.Response, error) {
	if s.closed.Load() {
		return nil, errors.New("provider is down")
	}
	if r.URL.Host != "oidc.test" {
		return nil, errors.New("unexpected provider host")
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, r)
	return rec.Result(), nil
}

func TestOIDCEnvironment(t *testing.T) {
	// Environment tests cannot run in parallel with each other.
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUTH_OIDC_ACME_ISSUER", "https://issuer.example/tenant")
	t.Setenv("AUTH_OIDC_ACME_CLIENT_ID", "client")
	t.Setenv("AUTH_OIDC_ACME_CLIENT_SECRET_FILE", secret)
	t.Setenv("AUTH_OIDC_ACME_SCOPES", "groups,email offline_access")
	t.Setenv("AUTH_OIDC_ACME_LABEL", "Acme")
	p := loadOIDCProvider("acme")
	if p.err != nil || p.secret != "from-file" || p.label != "Acme" || strings.Join(p.scopes, ",") != "openid,email,profile,groups,offline_access" {
		t.Fatalf("provider configuration: err=%v, scopes=%v", p.err, p.scopes)
	}
	t.Setenv("AUTH_OIDC_ACME_CLIENT_SECRET", "conflict")
	if loadOIDCProvider("acme").err == nil {
		t.Fatal("ambiguous secret accepted")
	}
}

func TestOIDCRoutesEnvironment(t *testing.T) {
	t.Setenv("AUTH_OIDC_PROVIDERS", "acme,other")
	t.Setenv("PUBLIC_ORIGIN", "http://app.test")
	t.Setenv("AUTH_OIDC_COOKIE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("AUTH_OIDC_ACME_ISSUER", "https://issuer.example")
	t.Setenv("AUTH_OIDC_ACME_CLIENT_ID", "client")
	t.Setenv("AUTH_OIDC_ACME_LABEL", "Acme")
	// A missing issuer affects only the other provider, without discovery at startup.
	a := &Auth{sessions: scs.New(), csrf: http.NewCrossOriginProtection(), logger: slog.Default(), now: time.Now}
	a.sessions.Cookie.Secure = false
	mux := http.NewServeMux()
	a.Routes(mux)
	handler := a.Middleware()(mux)
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/api/auth/oidc/providers", http.StatusOK},
		{"/api/auth/oidc/other/start", http.StatusServiceUnavailable},
		{"/api/auth/oidc/unknown/start", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://app.test"+tc.path, nil))
		if rec.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.path, rec.Code, rec.Body)
		}
		if tc.status == http.StatusOK && (!strings.Contains(rec.Body.String(), `"label":"Acme"`) || strings.Contains(rec.Body.String(), "client")) {
			t.Fatalf("provider list: %s", rec.Body)
		}
	}
}

func oidcDBFixture(t *testing.T) *fixture {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("OIDC database integration tests require DATABASE_URL")
	}
	f := newFixture(t)
	sql, err := os.ReadFile("../../internal/templates/packs/auth/files/api/db/migrations/00004_oidc_identities.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(sql), "-- +goose Down")
	if _, err := f.pool.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestOIDCAccounts(t *testing.T) {
	t.Parallel()
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%v", existing), func(t *testing.T) {
			t.Parallel()
			f := oidcDBFixture(t)
			var existingID int64
			var oldSession string
			if existing {
				oldSession = f.register(t, "person@example.com")
				if err := f.pool.QueryRow(t.Context(), `SELECT id FROM users WHERE email='person@example.com'`).Scan(&existingID); err != nil {
					t.Fatal(err)
				}
			}
			p := newOIDCTestProvider(t)
			app := newOIDCTestApp(t, p)
			app.h.auth = f.auth
			app.h.user = f.auth.oidcUser
			app.session = oldSession
			mux := http.NewServeMux()
			mux.Handle("GET /api/auth/oidc/{provider}/start", f.auth.endpoint(app.h.start))
			mux.Handle("GET /api/auth/oidc/{provider}/callback", f.auth.endpoint(app.h.callback))
			app.handler = f.auth.Middleware()(mux)
			cookie, callback := app.start(t, "/account")
			rec := app.callback(t, cookie, callback)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("callback: %d %s", rec.Code, rec.Body)
			}
			var id int64
			var password *string
			if err := f.pool.QueryRow(t.Context(), `SELECT u.id,u.password_hash FROM users u JOIN user_identities i ON i.user_id=u.id WHERE i.provider='test' AND i.subject='subject-1'`).Scan(&id, &password); err != nil {
				t.Fatal(err)
			}
			if existing && (id != existingID || password == nil) {
				t.Fatal("existing password account not preserved")
			}
			if !existing && password != nil {
				t.Fatal("new account has a password")
			}
			if c := sessionCookie(rec); c == nil {
				t.Fatal("no session")
			} else if rec := f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: c.Value}); rec.Code != http.StatusOK {
				t.Fatalf("session: %d %s", rec.Code, rec.Body)
			}
			if existing {
				if rec := f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: oldSession}); rec.Code != http.StatusUnauthorized {
					t.Fatalf("old session was not rotated: %d", rec.Code)
				}
			}
			// An email change never moves a known subject to a different account.
			again, err := f.auth.oidcUser(t.Context(), "test", "subject-1", "different@example.com", true)
			if err != nil || again.ID != id || again.Email != "person@example.com" {
				t.Fatalf("identity changed: %+v %v", again, err)
			}
			if !existing {
				rec := f.do(t, request{method: http.MethodPost, path: "/api/auth/login", body: creds("person@example.com", goodPassword)})
				if rec.Code != http.StatusUnauthorized || problemOf(t, rec).Detail != "The email address or password is incorrect." {
					t.Fatalf("passwordless login: %d %s", rec.Code, rec.Body)
				}
			}
		})
	}
}

func TestOIDCUnverifiedEmailNotLinked(t *testing.T) {
	t.Parallel()
	f := oidcDBFixture(t)
	f.register(t, "person@example.com")
	if _, err := f.auth.oidcUser(t.Context(), "test", "attacker", "person@example.com", false); err == nil {
		t.Fatal("unverified email linked")
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM user_identities`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("identities: %d %v", count, err)
	}
}

func TestOIDCConcurrentLink(t *testing.T) {
	t.Parallel()
	f := oidcDBFixture(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := f.auth.oidcUser(t.Context(), "test", "subject-1", "person@example.com", true); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var users, identities int
	if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM users),(SELECT count(*) FROM user_identities)`).Scan(&users, &identities); err != nil || users != 1 || identities != 1 {
		t.Fatalf("accounts: users=%d identities=%d error=%v", users, identities, err)
	}
}
