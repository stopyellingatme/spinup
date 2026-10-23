package cloudauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type provider struct {
	srv *httptest.Server

	mu         sync.Mutex
	challenge  string
	tokenForms []url.Values
	revokes    []url.Values
	revokeAuth []string
	refreshN   atomic.Int32
	validRef   string // the only refresh token the server accepts
	expiresIn  int
	// mutate lets a test change the authorize redirect query.
	mutate func(q url.Values)
}

func newProvider(t *testing.T) *provider {
	t.Helper()
	p := &provider{expiresIn: 3600}
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "cid" {
			http.Error(w, "bad authorize request", http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		p.challenge = q.Get("code_challenge")
		p.mu.Unlock()
		out := url.Values{"code": {"the-code"}, "state": {q.Get("state")}}
		if p.mutate != nil {
			p.mutate(out)
		}
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+out.Encode(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		p.mu.Lock()
		p.tokenForms = append(p.tokenForms, r.PostForm)
		p.mu.Unlock()
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.Header.Get("Accept") != "application/json" {
			http.Error(w, "bad headers", http.StatusBadRequest)
			return
		}
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			if Challenge(r.PostForm.Get("code_verifier")) != p.challenge || r.PostForm.Get("code") != "the-code" {
				writeJSON(w, 400, map[string]string{"error": "invalid_grant"})
				return
			}
			writeJSON(w, 200, map[string]any{"access_token": "a1", "refresh_token": "r1", "token_type": "bearer", "expires_in": p.expiresIn, "info": map[string]string{"name": "x"}})
		case "refresh_token":
			p.refreshN.Add(1)
			p.mu.Lock()
			ok := r.PostForm.Get("refresh_token") == p.validRef
			p.mu.Unlock()
			if !ok {
				writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": "used"})
				return
			}
			p.mu.Lock()
			p.validRef = "r2"
			p.mu.Unlock()
			writeJSON(w, 200, map[string]any{"access_token": "a2", "refresh_token": "r2", "expires_in": 3600})
		default:
			writeJSON(w, 400, map[string]string{"error": "unsupported_grant_type"})
		}
	})
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		p.mu.Lock()
		p.revokes = append(p.revokes, r.PostForm)
		p.revokeAuth = append(p.revokeAuth, r.Header.Get("Authorization"))
		p.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (p *provider) cfg() Config {
	return Config{
		AuthorizeURL: p.srv.URL + "/authorize",
		TokenURL:     p.srv.URL + "/token",
		RevokeURL:    p.srv.URL + "/revoke",
		ClientID:     "cid",
		Scopes:       []string{"read", "write"},
	}
}

func browser(t *testing.T) func(string) error {
	return func(u string) error {
		go func() {
			resp, err := get(u)
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
		return nil
	}
}

func TestChallengeRFC7636(t *testing.T) {
	got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")
	if want := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"; got != want {
		t.Fatalf("challenge = %s, want %s", got, want)
	}
	v, err := NewVerifier()
	if err != nil || len(v) != 43 {
		t.Fatalf("verifier = %q, %v", v, err)
	}
	s, _ := NewState()
	if len(s) != 43 {
		t.Fatalf("state length = %d", len(s))
	}
}

func TestTokenValid(t *testing.T) {
	now := time.Unix(1000, 0)
	if !(Token{AccessToken: "x", Expiry: now.Add(time.Hour)}).Valid(now, 5*time.Minute) {
		t.Error("fresh token must be valid")
	}
	if (Token{AccessToken: "x", Expiry: now.Add(time.Minute)}).Valid(now, 5*time.Minute) {
		t.Error("token inside the margin must not be valid")
	}
	if (Token{}).Valid(now, 0) {
		t.Error("empty token must not be valid")
	}
	if !(Token{AccessToken: "x"}).Valid(now, time.Hour) {
		t.Error("unknown expiry must be valid")
	}
}

func TestLoginEndToEnd(t *testing.T) {
	p := newProvider(t)
	var out bytes.Buffer
	tok, err := Login(context.Background(), p.cfg(), LoginOptions{Open: browser(t), Out: &out, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "a1" || tok.RefreshToken != "r1" || tok.ClientID != "cid" || tok.Expiry.IsZero() {
		t.Fatalf("token = %+v", tok)
	}
	if string(tok.Info) == "" {
		t.Error("info must be kept")
	}
	if !strings.Contains(out.String(), "/authorize?") || !strings.Contains(out.String(), "did not open") {
		t.Errorf("out = %q", out.String())
	}
	form := p.tokenForms[0]
	if form.Get("redirect_uri") == "" || !strings.HasPrefix(form.Get("redirect_uri"), "http://127.0.0.1:") || form.Get("client_id") != "cid" {
		t.Errorf("form = %v", form)
	}
	if _, ok := form["client_secret"]; ok {
		t.Error("client_secret must never be sent")
	}
}

func TestLoginStateMismatch(t *testing.T) {
	p := newProvider(t)
	p.mutate = func(q url.Values) { q.Set("state", "forged") }
	_, err := Login(context.Background(), p.cfg(), LoginOptions{Open: browser(t), Timeout: 10 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "state") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("err = %v", err)
	}
	if len(p.tokenForms) != 0 {
		t.Error("no exchange must happen")
	}
}

func TestLoginAccessDenied(t *testing.T) {
	p := newProvider(t)
	p.mutate = func(q url.Values) { q.Del("code"); q.Set("error", "access_denied") }
	_, err := Login(context.Background(), p.cfg(), LoginOptions{Open: browser(t), Timeout: 10 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "cancelled in the browser") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoginIgnoresStrayRequests(t *testing.T) {
	p := newProvider(t)
	open := func(u string) error {
		go func() {
			parsed, _ := url.Parse(u)
			redirect := parsed.Query().Get("redirect_uri")
			base := strings.TrimSuffix(redirect, "/callback")
			if resp, err := get(base + "/other"); err == nil {
				if resp.StatusCode != http.StatusNotFound {
					t.Errorf("other path status = %d", resp.StatusCode)
				}
				_ = resp.Body.Close()
			}
			if resp, err := get(redirect); err == nil { // no state
				if resp.StatusCode != http.StatusBadRequest {
					t.Errorf("no state status = %d", resp.StatusCode)
				}
				if csp := resp.Header.Get("Content-Security-Policy"); !strings.HasPrefix(csp, "default-src 'none'") {
					t.Errorf("csp = %q", csp)
				}
				_ = resp.Body.Close()
			}
			if resp, err := get(u); err == nil {
				_ = resp.Body.Close()
			}
		}()
		return nil
	}
	tok, err := Login(context.Background(), p.cfg(), LoginOptions{Open: open, Timeout: 10 * time.Second})
	if err != nil || tok.AccessToken != "a1" {
		t.Fatalf("tok = %+v, err = %v", tok, err)
	}
}

func TestLoginTimeoutAndCancel(t *testing.T) {
	p := newProvider(t)
	_, err := Login(context.Background(), p.cfg(), LoginOptions{Timeout: 50 * time.Millisecond, TryHint: "use --no-browser or set TOKEN_VAR"})
	if err == nil || !strings.Contains(err.Error(), "Try: use --no-browser or set TOKEN_VAR") {
		t.Fatalf("err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	_, err = Login(ctx, p.cfg(), LoginOptions{Timeout: 10 * time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestExchangeRefreshRevokeBodies(t *testing.T) {
	p := newProvider(t)
	p.validRef = "rr"
	cfg := p.cfg()
	now := time.Unix(5000, 0)
	cfg.Now = func() time.Time { return now }
	ctx := context.Background()

	tok, err := Refresh(ctx, cfg, "rr")
	if err != nil {
		t.Fatal(err)
	}
	if !tok.Expiry.Equal(now.Add(time.Hour)) || tok.RefreshToken != "r2" {
		t.Fatalf("tok = %+v", tok)
	}
	f := p.tokenForms[0]
	if f.Get("grant_type") != "refresh_token" || f.Get("refresh_token") != "rr" || f.Get("client_id") != "cid" || len(f) != 3 {
		t.Errorf("refresh form = %v", f)
	}
	if err := Revoke(ctx, cfg, "tok1"); err != nil {
		t.Fatal(err)
	}
	if p.revokes[0].Get("token") != "tok1" || p.revokeAuth[0] != "Bearer tok1" {
		t.Errorf("revoke = %v %v", p.revokes, p.revokeAuth)
	}
	if _, err := Exchange(ctx, cfg, "bad", "v", "http://127.0.0.1:1/callback"); !errors.Is(err, ErrReauth) {
		t.Errorf("exchange err = %v", err)
	}
	g := p.tokenForms[1]
	for _, k := range []string{"grant_type", "code", "client_id", "code_verifier", "redirect_uri"} {
		if g.Get(k) == "" {
			t.Errorf("exchange form lacks %s", k)
		}
	}
	if _, ok := g["client_secret"]; ok {
		t.Error("client_secret must never be sent")
	}
}

func TestRefreshInvalidGrantAndNoTokenInErrors(t *testing.T) {
	p := newProvider(t)
	p.validRef = "other"
	_, err := Refresh(context.Background(), p.cfg(), "secret-refresh-value")
	if !errors.Is(err, ErrReauth) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret-refresh-value") {
		t.Error("error must not contain the token")
	}
}

func TestUnknownExpiry(t *testing.T) {
	p := newProvider(t)
	p.expiresIn = 0
	p.challenge = Challenge("v")
	tok, err := Exchange(context.Background(), p.cfg(), "the-code", "v", "http://127.0.0.1:1/callback")
	if err != nil || !tok.Expiry.IsZero() {
		t.Fatalf("tok = %+v, err = %v", tok, err)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	st := Store{Dir: filepath.Join(t.TempDir(), "creds"), Name: "x.json"}
	if _, found, err := st.Load(); found || err != nil {
		t.Fatalf("empty load = %v %v", found, err)
	}
	in := Token{AccessToken: "a", RefreshToken: "r", Expiry: time.Unix(99, 0).UTC(), ClientID: "cid"}
	if err := st.Save(in); err != nil {
		t.Fatal(err)
	}
	out, found, err := st.Load()
	if err != nil || !found || out.AccessToken != "a" || !out.Expiry.Equal(in.Expiry) {
		t.Fatalf("load = %+v %v %v", out, found, err)
	}
	if runtime.GOOS != "windows" {
		di, _ := os.Stat(st.Dir)
		fi, _ := os.Stat(filepath.Join(st.Dir, st.Name))
		if di.Mode().Perm() != 0o700 || fi.Mode().Perm() != 0o600 {
			t.Errorf("modes = %v %v", di.Mode().Perm(), fi.Mode().Perm())
		}
		p := filepath.Join(st.Dir, st.Name)
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err := st.Load()
		if err == nil || !strings.Contains(err.Error(), "Try: chmod 600 "+p) {
			t.Fatalf("err = %v", err)
		}
		if err := st.Save(in); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.Load(); err != nil {
			t.Fatalf("save must fix the mode: %v", err)
		}
	}
	if err := st.Delete(); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestDefaultDir(t *testing.T) {
	t.Setenv("SPINUP_CREDENTIALS_DIR", "/x/y")
	if d, _ := DefaultDir(); d != "/x/y" {
		t.Errorf("dir = %s", d)
	}
	t.Setenv("SPINUP_CREDENTIALS_DIR", "")
	t.Setenv("HOME", "/home/u")
	t.Setenv("USERPROFILE", "/home/u")
	d, err := DefaultDir()
	if err != nil || filepath.ToSlash(d) != "/home/u/.config/spinup/credentials" {
		t.Errorf("dir = %s %v", d, err)
	}
}

func TestWithLockCancel(t *testing.T) {
	st := Store{Dir: t.TempDir(), Name: "x.json"}
	err := st.WithLock(context.Background(), func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		if err := st.WithLock(ctx, func() error { return nil }); err == nil {
			t.Error("second lock must fail")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WithLock(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("lock must be free again: %v", err)
	}
}

func newSource(p *provider, dir string) *Source {
	return &Source{
		Getenv:   func(string) string { return "" },
		EnvNames: []string{"A_TOKEN", "B_TOKEN"},
		Store:    Store{Dir: dir, Name: "x.json"},
		Config:   p.cfg(),
		TryHint:  "export A_TOKEN",
	}
}

func TestSourceEnvPrecedence(t *testing.T) {
	p := newProvider(t)
	s := newSource(p, t.TempDir())
	var warn bytes.Buffer
	s.Warn = &warn
	env := map[string]string{"B_TOKEN": "bbb"}
	s.Getenv = func(k string) string { return env[k] }
	tok, from, err := s.Current(context.Background())
	if err != nil || tok.AccessToken != "bbb" || from != "env:B_TOKEN" || warn.Len() != 0 {
		t.Fatalf("%v %s %v %q", tok, from, err, warn.String())
	}
	env["A_TOKEN"] = "aaa"
	for range 2 {
		tok, from, _ = s.Current(context.Background())
		if tok.AccessToken != "aaa" || from != "env:A_TOKEN" {
			t.Fatalf("%v %s", tok, from)
		}
	}
	if strings.Count(warn.String(), "warning:") != 1 {
		t.Errorf("warn = %q", warn.String())
	}
	if _, found, _ := s.Store.Load(); found {
		t.Error("env token must not be stored")
	}
}

func seed(t *testing.T, s *Source, tok Token) {
	t.Helper()
	if err := s.Store.Save(tok); err != nil {
		t.Fatal(err)
	}
}

func TestSourceRefreshRotationPersists(t *testing.T) {
	p := newProvider(t)
	p.validRef = "r1"
	s := newSource(p, t.TempDir())
	seed(t, s, Token{AccessToken: "old", RefreshToken: "r1", Expiry: time.Now().Add(-time.Minute), Info: json.RawMessage(`{"k":1}`)})
	got, err := s.Token(context.Background())
	if err != nil || got != "a2" {
		t.Fatalf("got %q %v", got, err)
	}
	saved, _, _ := s.Store.Load()
	if saved.RefreshToken != "r2" || saved.AccessToken != "a2" || strings.Join(strings.Fields(string(saved.Info)), "") != `{"k":1}` {
		t.Fatalf("saved = %+v", saved)
	}
	// The cached token serves the next call with no second refresh.
	if got, _ = s.Token(context.Background()); got != "a2" || p.refreshN.Load() != 1 {
		t.Fatalf("refreshes = %d", p.refreshN.Load())
	}
}

func TestSourceConcurrentOneRefresh(t *testing.T) {
	p := newProvider(t)
	p.validRef = "r1"
	dir := t.TempDir()
	s1, s2 := newSource(p, dir), newSource(p, dir)
	seed(t, s1, Token{AccessToken: "old", RefreshToken: "r1", Expiry: time.Now().Add(-time.Minute)})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, s := range []*Source{s1, s2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.Token(context.Background())
			if err == nil && got != "a2" {
				err = fmt.Errorf("got %q", got)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := p.refreshN.Load(); n != 1 {
		t.Fatalf("refresh requests = %d, want 1", n)
	}
}

func TestSourceReauthDeletesAndFails(t *testing.T) {
	p := newProvider(t)
	p.validRef = "nope"
	s := newSource(p, t.TempDir())
	seed(t, s, Token{AccessToken: "old", RefreshToken: "r1", Expiry: time.Now().Add(-time.Minute)})
	_, err := s.Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Try: export A_TOKEN") {
		t.Fatalf("err = %v", err)
	}
	if _, found, _ := s.Store.Load(); found {
		t.Error("file must be deleted")
	}
}

func TestSourceInteractiveLogin(t *testing.T) {
	p := newProvider(t)
	s := newSource(p, t.TempDir())
	s.Interactive = true
	s.Login = func(ctx context.Context) (Token, error) {
		return Login(ctx, p.cfg(), LoginOptions{Open: browser(t), Timeout: 10 * time.Second})
	}
	tok, from, err := s.Current(context.Background())
	if err != nil || tok.AccessToken != "a1" || from != "login" {
		t.Fatalf("%+v %s %v", tok, from, err)
	}
	if saved, found, _ := s.Store.Load(); !found || saved.RefreshToken != "r1" {
		t.Errorf("saved = %+v", saved)
	}
	// A new Source reads the store.
	s2 := newSource(p, s.Store.Dir)
	if _, from, _ := s2.Current(context.Background()); from != "store" {
		t.Errorf("from = %s", from)
	}
}

func TestSourceNonInteractiveNoToken(t *testing.T) {
	s := newSource(newProvider(t), t.TempDir())
	if _, err := s.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "Try: export A_TOKEN") {
		t.Fatalf("err = %v", err)
	}
}

func TestSourceLogout(t *testing.T) {
	p := newProvider(t)
	s := newSource(p, t.TempDir())
	seed(t, s, Token{AccessToken: "acc", RefreshToken: "ref", Expiry: time.Now().Add(time.Hour)})
	if err := s.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.revokes) != 2 || p.revokes[0].Get("token") != "ref" || p.revokes[1].Get("token") != "acc" {
		t.Fatalf("revokes = %v", p.revokes)
	}
	if _, found, _ := s.Store.Load(); found {
		t.Error("file must be deleted")
	}
	if err := s.Logout(context.Background()); err != nil {
		t.Fatalf("logout with no file: %v", err)
	}
}

func TestSourceLogoutReportsFailedRevoke(t *testing.T) {
	p := newProvider(t)
	s := newSource(p, t.TempDir())
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"server_error"}`, http.StatusInternalServerError)
	}))
	defer down.Close()
	s.Config.RevokeURL = down.URL
	seed(t, s, Token{AccessToken: "secret-access-1", RefreshToken: "secret-refresh-1", Expiry: time.Now().Add(time.Hour)})
	err := s.Logout(context.Background())
	if !errors.Is(err, ErrNotRevoked) {
		t.Fatalf("err = %v, want ErrNotRevoked", err)
	}
	if strings.Contains(err.Error(), "secret-access-1") || strings.Contains(err.Error(), "secret-refresh-1") {
		t.Errorf("error leaks a token: %v", err)
	}
	if _, found, _ := s.Store.Load(); found {
		t.Error("file must be deleted also when the revoke fails")
	}
}

func get(u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}
