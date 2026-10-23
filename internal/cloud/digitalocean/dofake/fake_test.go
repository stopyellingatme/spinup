package dofake

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newReq(t *testing.T, method, target, body, ctype string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	return req
}

var noFollow = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// authorize drives the authorize endpoint and returns the redirect query.
func authorize(t *testing.T, base string, extra url.Values) (int, url.Values) {
	t.Helper()
	q := url.Values{
		"response_type": {"code"}, "client_id": {"spinup-test-client"},
		"redirect_uri": {"http://127.0.0.1:5555/callback"}, "state": {"st"},
		"code_challenge_method": {"S256"}, "code_challenge": {challenge("verifier-1")}, "scope": {"account:read"},
	}
	for k, v := range extra {
		q[k] = v
	}
	resp, err := noFollow.Do(newReq(t, "GET", base+"/v1/oauth/authorize?"+q.Encode(), "", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc == nil {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, loc.Query()
}

type tokenReply struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	Info         struct {
		Email string `json:"email"`
	} `json:"info"`
}

func postToken(t *testing.T, base string, form url.Values) (int, tokenReply) {
	t.Helper()
	resp, err := http.DefaultClient.Do(newReq(t, "POST", base+"/v1/oauth/token", form.Encode(), "application/x-www-form-urlencoded"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tr tokenReply
	_ = json.NewDecoder(resp.Body).Decode(&tr)
	return resp.StatusCode, tr
}

func exchange(t *testing.T, base, code, verifier string) (int, tokenReply) {
	return postToken(t, base, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {"spinup-test-client"}, "code": {code},
		"redirect_uri": {"http://127.0.0.1:5555/callback"}, "code_verifier": {verifier},
	})
}

func apiStatus(t *testing.T, base, token string) int {
	t.Helper()
	req := newReq(t, "GET", base+"/v2/account", "", "")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func start(t *testing.T, o Options) (*Server, string) {
	s := New(o)
	url := s.Start()
	t.Cleanup(s.Close)
	return s, url
}

func TestAuthorizeAndExchange(t *testing.T) {
	s, base := start(t, Options{Email: "me@example.com"})
	status, q := authorize(t, base, nil)
	if status != 302 || q.Get("code") == "" || q.Get("state") != "st" {
		t.Fatalf("authorize: %d %v", status, q)
	}
	code, tr := exchange(t, base, q.Get("code"), "verifier-1")
	if code != 200 || tr.AccessToken == "" || tr.RefreshToken == "" || tr.TokenType != "bearer" || tr.ExpiresIn != 3600 || tr.Info.Email != "me@example.com" || tr.Scope != "account:read" {
		t.Fatalf("exchange: %d %+v", code, tr)
	}
	if apiStatus(t, base, tr.AccessToken) != 200 || s.TokensIssued() != 1 {
		t.Fatal("the access token must work")
	}
	if apiStatus(t, base, "") != 401 || apiStatus(t, base, "nope") != 401 {
		t.Fatal("a bad token must answer 401")
	}
}

func TestAuthorizeRejects(t *testing.T) {
	_, base := start(t, Options{})
	for name, extra := range map[string]url.Values{
		"https redirect":  {"redirect_uri": {"https://127.0.0.1/callback"}},
		"remote redirect": {"redirect_uri": {"http://example.com/callback"}},
		"wrong path":      {"redirect_uri": {"http://127.0.0.1:1/other"}},
		"wrong client":    {"client_id": {"other"}},
	} {
		status, _ := authorize(t, base, extra)
		if status != 400 {
			t.Errorf("%s: status %d, want 400", name, status)
		}
	}
	for name, extra := range map[string]url.Values{
		"plain method": {"code_challenge_method": {"plain"}},
		"no challenge": {"code_challenge": {""}},
		"wrong type":   {"response_type": {"token"}},
	} {
		status, q := authorize(t, base, extra)
		if status != 302 || q.Get("error") == "" || q.Get("code") != "" {
			t.Errorf("%s: %d %v", name, status, q)
		}
	}
	for _, uri := range []string{"http://localhost:1/callback", "http://[::1]:2/callback"} {
		if status, q := authorize(t, base, url.Values{"redirect_uri": {uri}}); status != 302 || q.Get("code") == "" {
			t.Errorf("%s: %d %v", uri, status, q)
		}
	}
}

func TestDeny(t *testing.T) {
	_, base := start(t, Options{Deny: true})
	status, q := authorize(t, base, nil)
	if status != 302 || q.Get("error") != "access_denied" || q.Get("state") != "st" {
		t.Fatalf("deny: %d %v", status, q)
	}
}

func TestPKCEMismatchAndCodeReuse(t *testing.T) {
	_, base := start(t, Options{})
	_, q := authorize(t, base, nil)
	if code, tr := exchange(t, base, q.Get("code"), "wrong-verifier"); code != 400 || tr.Error != "invalid_grant" {
		t.Fatalf("mismatch: %d %+v", code, tr)
	}
	// A failed try burns the code.
	if code, _ := exchange(t, base, q.Get("code"), "verifier-1"); code != 400 {
		t.Fatalf("burned code accepted: %d", code)
	}
	_, q = authorize(t, base, nil)
	if code, _ := exchange(t, base, q.Get("code"), "verifier-1"); code != 200 {
		t.Fatalf("first use: %d", code)
	}
	if code, tr := exchange(t, base, q.Get("code"), "verifier-1"); code != 400 || tr.Error != "invalid_grant" {
		t.Fatalf("reuse: %d %+v", code, tr)
	}
}

func TestExchangeChecksClientAndRedirect(t *testing.T) {
	_, base := start(t, Options{})
	_, q := authorize(t, base, nil)
	code, _ := postToken(t, base, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {"other"}, "code": {q.Get("code")},
		"redirect_uri": {"http://127.0.0.1:5555/callback"}, "code_verifier": {"verifier-1"},
	})
	if code != 401 {
		t.Fatalf("wrong client: %d", code)
	}
	_, q = authorize(t, base, nil)
	code, _ = postToken(t, base, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {"spinup-test-client"}, "code": {q.Get("code")},
		"redirect_uri": {"http://127.0.0.1:6666/callback"}, "code_verifier": {"verifier-1"},
	})
	if code != 400 {
		t.Fatalf("redirect mismatch: %d", code)
	}
}

func TestRefreshRotation(t *testing.T) {
	s, base := start(t, Options{})
	_, q := authorize(t, base, nil)
	_, first := exchange(t, base, q.Get("code"), "verifier-1")
	refresh := func(tok string) (int, tokenReply) {
		return postToken(t, base, url.Values{"grant_type": {"refresh_token"}, "client_id": {"spinup-test-client"}, "refresh_token": {tok}})
	}
	code, second := refresh(first.RefreshToken)
	if code != 200 || second.AccessToken == first.AccessToken || second.RefreshToken == first.RefreshToken || second.Scope != "account:read" {
		t.Fatalf("refresh: %d %+v", code, second)
	}
	if code, tr := refresh(first.RefreshToken); code != 400 || tr.Error != "invalid_grant" {
		t.Fatalf("old refresh: %d %+v", code, tr)
	}
	if apiStatus(t, base, first.AccessToken) != 401 {
		t.Fatal("the old access token must stop working")
	}
	if apiStatus(t, base, second.AccessToken) != 200 || s.TokensIssued() != 2 {
		t.Fatal("the new access token must work")
	}
}

func TestAccessTokenExpires(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	_, base := start(t, Options{Now: func() time.Time { return now }, AccessTTL: time.Minute})
	_, q := authorize(t, base, nil)
	_, tr := exchange(t, base, q.Get("code"), "verifier-1")
	if apiStatus(t, base, tr.AccessToken) != 200 {
		t.Fatal("fresh token must work")
	}
	now = now.Add(2 * time.Minute)
	if apiStatus(t, base, tr.AccessToken) != 401 {
		t.Fatal("expired token must answer 401")
	}
}

func TestRevoke(t *testing.T) {
	_, base := start(t, Options{})
	_, q := authorize(t, base, nil)
	_, tr := exchange(t, base, q.Get("code"), "verifier-1")
	resp, err := http.DefaultClient.Do(newReq(t, "POST", base+"/v1/oauth/revoke", url.Values{"token": {tr.RefreshToken}}.Encode(), "application/x-www-form-urlencoded"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || apiStatus(t, base, tr.AccessToken) != 401 {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if code, _ := postToken(t, base, url.Values{"grant_type": {"refresh_token"}, "client_id": {"spinup-test-client"}, "refresh_token": {tr.RefreshToken}}); code != 400 {
		t.Fatalf("revoked refresh accepted: %d", code)
	}
}

func TestRegister(t *testing.T) {
	_, base := start(t, Options{})
	post := func(body string) (int, map[string]any) {
		resp, err := http.DefaultClient.Do(newReq(t, "POST", base+"/v1/oauth/register", body, "application/json"))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		b, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(b, &m)
		return resp.StatusCode, m
	}
	code, m := post(`{"client_name":"spinup","token_endpoint_auth_method":"none"}`)
	if code != 201 || m["client_id"] != "spinup-test-client" || m["registration_access_token"] == "" {
		t.Fatalf("register: %d %v", code, m)
	}
	if code, _ := post(`{"token_endpoint_auth_method":"client_secret_basic"}`); code != 400 {
		t.Fatalf("secret client: %d", code)
	}
}

func TestStaticTokenAndControls(t *testing.T) {
	s, base := start(t, Options{StaticTokens: []string{"pat"}})
	if apiStatus(t, base, "pat") != 200 {
		t.Fatal("static token must work")
	}
	s.FailNext("GET", "/v2/account", 503, 1)
	if apiStatus(t, base, "pat") != 503 || apiStatus(t, base, "pat") != 200 {
		t.Fatal("FailNext must fail once")
	}
	s.RateLimit(1)
	if apiStatus(t, base, "pat") != 429 || apiStatus(t, base, "pat") != 200 {
		t.Fatal("RateLimit must fail once")
	}
	for _, r := range s.Requests() {
		if strings.Contains(r.Path, "pat") {
			t.Fatal("the log must hold no token")
		}
	}
}
