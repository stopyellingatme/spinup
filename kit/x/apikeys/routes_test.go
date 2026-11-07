package apikeys

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/httpx"
)

const routePassword = "correct horse battery"

type routeFixture struct {
	t     *testing.T
	pool  *pgxpool.Pool
	store *Store
	h     http.Handler
}

type call struct {
	method, path, body string
	cookie, bearer     string
	contentType        string // the default is application/json when a body is present
}

func newRouteFixture(t *testing.T, cfg Config) *routeFixture {
	t.Helper()
	t.Setenv("AUTH_RATE_LIMIT", "off")
	pool := keyDB(t, false)
	s := dbStore(t, pool, cfg)
	a, err := auth.New(pool, auth.WithSecureCookie(false), auth.WithBearer(s))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(context.Background()) })
	mux := http.NewServeMux()
	a.Routes(mux)
	s.Routes(a)(mux)
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	return &routeFixture{t: t, pool: pool, store: s, h: httpx.Chain(mux, a.Middleware(), s.Middleware())}
}

func (f *routeFixture) do(c call) *httptest.ResponseRecorder {
	f.t.Helper()
	var body *strings.Reader
	if c.body != "" {
		body = strings.NewReader(c.body)
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequestWithContext(f.t.Context(), c.method, "http://api.test"+c.path, body)
	if c.body != "" {
		ct := c.contentType
		if ct == "" {
			ct = "application/json"
		}
		r.Header.Set("Content-Type", ct)
	}
	if c.cookie != "" {
		r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: c.cookie})
	}
	if c.bearer != "" {
		r.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

// register signs a user up and returns its session cookie. A non-empty role
// replaces the default role in the database.
func (f *routeFixture) register(email, role string) string {
	f.t.Helper()
	w := f.do(call{method: "POST", path: "/api/auth/register", body: `{"email":"` + email + `","password":"` + routePassword + `"}`})
	if w.Code != http.StatusCreated {
		f.t.Fatalf("register %s: %d %s", email, w.Code, w.Body)
	}
	if role != "" {
		if _, err := f.pool.Exec(f.t.Context(), `UPDATE users SET role=$2 WHERE email=$1`, email, role); err != nil {
			f.t.Fatal(err)
		}
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c.Value
		}
	}
	f.t.Fatalf("register %s: no session cookie", email)
	return ""
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", w.Body, err)
	}
	return out
}

func expect(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func fieldsOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem %q: %v", w.Body, err)
	}
	var names []string
	for _, e := range p.Errors {
		names = append(names, e.Field)
	}
	return strings.Join(names, ",")
}

func TestRoutesGuards(t *testing.T) {
	f := newRouteFixture(t, testConfig())
	user := f.register("guard@example.com", "")
	for _, tc := range []struct {
		method, path, body string
	}{
		{"GET", "/api/me/keys", ""},
		{"POST", "/api/me/keys", `{"name":"a","scope":"read"}`},
		{"DELETE", "/api/me/keys/1", ""},
		{"GET", "/api/admin/apikeys", ""},
		{"PUT", "/api/admin/apikeys/users/guard@example.com/plan", `{"plan":"free"}`},
		{"DELETE", "/api/admin/apikeys/keys/1", ""},
	} {
		expect(t, f.do(call{method: tc.method, path: tc.path, body: tc.body}), 401)
		if strings.HasPrefix(tc.path, "/api/admin/") {
			expect(t, f.do(call{method: tc.method, path: tc.path, body: tc.body, cookie: user}), 403)
		}
	}
}

func TestRoutesKeyLifecycle(t *testing.T) {
	cfg := testConfig()
	cfg.MaxKeys = 2
	f := newRouteFixture(t, cfg)
	alice := f.register("alice@example.com", "")
	bob := f.register("bob@example.com", "")

	w := f.do(call{method: "GET", path: "/api/me/keys", cookie: alice})
	expect(t, w, 200)
	got := decode(t, w)
	plan, _ := got["plan"].(map[string]any)
	if plan["name"] != "free" || plan["per_minute"] != float64(60) || plan["per_day"] != float64(1000) {
		t.Errorf("plan = %v", got["plan"])
	}
	if got["max_keys"] != float64(2) || got["today"] != float64(0) {
		t.Errorf("max_keys/today = %v/%v", got["max_keys"], got["today"])
	}
	if keys, ok := got["keys"].([]any); !ok || len(keys) != 0 {
		t.Errorf("keys = %v, want an empty list", got["keys"])
	}
	if usage, ok := got["usage"].([]any); !ok || len(usage) != 0 {
		t.Errorf("usage = %v, want an empty list", got["usage"])
	}

	w = f.do(call{method: "POST", path: "/api/me/keys", body: `{"name":"first","scope":"write","expires_in_days":7}`, cookie: alice})
	expect(t, w, 201)
	created := decode(t, w)
	key, _ := created["key"].(map[string]any)
	token, _ := created["token"].(string)
	if !strings.HasPrefix(token, "key_") || key["name"] != "first" || key["scope"] != "write" || key["expires_at"] == nil || key["display"] == "" {
		t.Errorf("created = %v", created)
	}
	if strings.Contains(w.Body.String(), "user_id") {
		t.Errorf("the answer exposes the user ID: %s", w.Body)
	}
	id := strconv.FormatInt(int64(key["id"].(float64)), 10)

	expect(t, f.do(call{method: "POST", path: "/api/me/keys", body: `{"name":"second","scope":"read"}`, cookie: alice}), 201)
	w = f.do(call{method: "POST", path: "/api/me/keys", body: `{"name":"third","scope":"read"}`, cookie: alice})
	expect(t, w, 422)
	if !strings.Contains(w.Body.String(), "maximum of 2 keys") || !strings.Contains(w.Body.String(), "Try:") {
		t.Errorf("max keys detail: %s", w.Body)
	}

	w = f.do(call{method: "GET", path: "/api/me/keys", cookie: alice})
	expect(t, w, 200)
	if keys, _ := decode(t, w)["keys"].([]any); len(keys) != 2 {
		t.Errorf("keys = %v, want 2", keys)
	}
	if keys, _ := decode(t, f.do(call{method: "GET", path: "/api/me/keys", cookie: bob}))["keys"].([]any); len(keys) != 0 {
		t.Errorf("bob sees %v", keys)
	}

	// Another user cannot revoke the key, and a bad ID is a 404.
	expect(t, f.do(call{method: "DELETE", path: "/api/me/keys/" + id, cookie: bob}), 404)
	expect(t, f.do(call{method: "DELETE", path: "/api/me/keys/nope", cookie: alice}), 404)
	expect(t, f.do(call{method: "DELETE", path: "/api/me/keys/-4", cookie: alice}), 404)
	expect(t, f.do(call{method: "DELETE", path: "/api/me/keys/999999", cookie: alice}), 404)
	w = f.do(call{method: "DELETE", path: "/api/me/keys/" + id, cookie: alice})
	expect(t, w, 204)
	expect(t, f.do(call{method: "DELETE", path: "/api/me/keys/" + id, cookie: alice}), 404)
}

func TestRoutesCreateValidation(t *testing.T) {
	f := newRouteFixture(t, testConfig())
	cookie := f.register("valid@example.com", "")
	for _, tc := range []struct {
		name, body, ctype string
		status            int
		fields            string
	}{
		{"not json", `{"name":"a","scope":"read"}`, "text/plain", 415, ""},
		{"empty name", `{"name":"","scope":"read"}`, "", 422, "name"},
		{"line break", `{"name":"a\nb","scope":"read"}`, "", 422, "name"},
		{"long name", `{"name":"` + strings.Repeat("x", 65) + `","scope":"read"}`, "", 422, "name"},
		{"bad scope", `{"name":"a","scope":"admin"}`, "", 422, "scope"},
		{"bad days", `{"name":"a","scope":"read","expires_in_days":366}`, "", 422, "expires_in_days"},
		{"negative days", `{"name":"a","scope":"read","expires_in_days":-1}`, "", 422, "expires_in_days"},
		{"all bad", `{"name":"","scope":"x","expires_in_days":999}`, "", 422, "name,scope,expires_in_days"},
		{"missing scope", `{"name":"a"}`, "", 422, "scope"},
		{"unknown field", `{"name":"a","scope":"read","extra":1}`, "", 400, ""},
		{"too large", `{"name":"` + strings.Repeat("x", 17<<10) + `","scope":"read"}`, "", 413, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := f.do(call{method: "POST", path: "/api/me/keys", body: tc.body, contentType: tc.ctype, cookie: cookie})
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body)
			}
			if got := w.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q", got)
			}
			if tc.fields != "" && fieldsOf(t, w) != tc.fields {
				t.Errorf("fields = %q, want %q: %s", fieldsOf(t, w), tc.fields, w.Body)
			}
		})
	}
}

func TestRoutesAdmin(t *testing.T) {
	cfg := testConfig()
	cfg.Plans["pro"] = Plan{PerMinute: 600, PerDay: 100000}
	f := newRouteFixture(t, cfg)
	admin := f.register("boss@example.com", "manager")
	user := f.register("member@example.com", "")
	w := f.do(call{method: "POST", path: "/api/me/keys", body: `{"name":"member key","scope":"read"}`, cookie: user})
	expect(t, w, 201)
	keyID := strconv.FormatInt(int64(decode(t, w)["key"].(map[string]any)["id"].(float64)), 10)

	w = f.do(call{method: "GET", path: "/api/admin/apikeys", cookie: admin})
	expect(t, w, 200)
	got := decode(t, w)
	if got["default_plan"] != "free" {
		t.Errorf("default_plan = %v", got["default_plan"])
	}
	plans, _ := got["plans"].([]any)
	if len(plans) != 2 || plans[0].(map[string]any)["name"] != "free" || plans[1].(map[string]any)["name"] != "pro" || plans[1].(map[string]any)["per_day"] != float64(100000) {
		t.Errorf("plans = %v", got["plans"])
	}
	accounts, _ := got["accounts"].([]any)
	if len(accounts) != 1 {
		t.Fatalf("accounts = %v", got["accounts"])
	}
	acc := accounts[0].(map[string]any)
	if acc["email"] != "member@example.com" || acc["plan"] != "free" || len(acc["keys"].([]any)) != 1 {
		t.Errorf("account = %v", acc)
	}

	path := "/api/admin/apikeys/users/member@example.com/plan"
	w = f.do(call{method: "PUT", path: path, body: `{"plan":"pro"}`, cookie: admin})
	expect(t, w, 200)
	if acc := decode(t, w); acc["email"] != "member@example.com" || acc["plan"] != "pro" {
		t.Errorf("plan answer = %v", acc)
	}
	w = f.do(call{method: "GET", path: "/api/me/keys", cookie: user})
	expect(t, w, 200)
	if plan := decode(t, w)["plan"].(map[string]any); plan["name"] != "pro" {
		t.Errorf("plan = %v", plan)
	}
	// A mixed-case email and an escaped address find the same user.
	expect(t, f.do(call{method: "PUT", path: "/api/admin/apikeys/users/Member%40Example.com/plan", body: `{"plan":"free"}`, cookie: admin}), 200)

	w = f.do(call{method: "PUT", path: path, body: `{"plan":"gold"}`, cookie: admin})
	expect(t, w, 422)
	if fieldsOf(t, w) != "plan" {
		t.Errorf("fields = %q", fieldsOf(t, w))
	}
	expect(t, f.do(call{method: "PUT", path: path, body: `{"plan":"pro"}`, contentType: "text/plain", cookie: admin}), 415)
	w = f.do(call{method: "PUT", path: path, body: `{}`, cookie: admin})
	expect(t, w, 422)
	expect(t, f.do(call{method: "PUT", path: "/api/admin/apikeys/users/nobody@example.com/plan", body: `{"plan":"pro"}`, cookie: admin}), 404)

	expect(t, f.do(call{method: "DELETE", path: "/api/admin/apikeys/keys/nope", cookie: admin}), 404)
	expect(t, f.do(call{method: "DELETE", path: "/api/admin/apikeys/keys/999999", cookie: admin}), 404)
	expect(t, f.do(call{method: "DELETE", path: "/api/admin/apikeys/keys/" + keyID, cookie: admin}), 204)
	expect(t, f.do(call{method: "DELETE", path: "/api/admin/apikeys/keys/" + keyID, cookie: admin}), 404)
	if keys := decode(t, f.do(call{method: "GET", path: "/api/me/keys", cookie: user}))["keys"].([]any); len(keys) != 0 {
		t.Errorf("keys after the admin revoke = %v", keys)
	}
}

func TestRoutesBearerEndToEnd(t *testing.T) {
	f := newRouteFixture(t, testConfig())
	cookie := f.register("bearer@example.com", "")
	w := f.do(call{method: "POST", path: "/api/me/keys", body: `{"name":"e2e","scope":"read"}`, cookie: cookie})
	expect(t, w, 201)
	created := decode(t, w)
	token := created["token"].(string)
	id := strconv.FormatInt(int64(created["key"].(map[string]any)["id"].(float64)), 10)

	w = f.do(call{method: "GET", path: "/api/session", bearer: token})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "bearer@example.com") {
		t.Fatalf("session with a key: %d %s", w.Code, w.Body)
	}
	// A key cannot manage keys or call admin routes.
	if w = f.do(call{method: "GET", path: "/api/me/keys", bearer: token}); w.Code != 403 {
		t.Errorf("GET /api/me/keys with a key: %d %s", w.Code, w.Body)
	}
	if w = f.do(call{method: "POST", path: "/api/me/keys", body: `{"name":"x","scope":"read"}`, bearer: token}); w.Code != 403 {
		t.Errorf("POST /api/me/keys with a key: %d %s", w.Code, w.Body)
	}
	if w = f.do(call{method: "GET", path: "/api/admin/apikeys", bearer: token}); w.Code != 403 {
		t.Errorf("admin route with a key: %d %s", w.Code, w.Body)
	}

	// The cookie session counts what this process has not flushed.
	w = f.do(call{method: "GET", path: "/api/me/keys", cookie: cookie})
	expect(t, w, 200)
	if today := decode(t, w)["today"]; today != float64(1) {
		t.Errorf("today = %v, want 1 (one counted request, unflushed)", today)
	}
	if err := f.store.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	w = f.do(call{method: "GET", path: "/api/me/keys", cookie: cookie})
	got := decode(t, w)
	if got["today"] != float64(1) {
		t.Errorf("today after the flush = %v, want 1", got["today"])
	}
	if usage, _ := got["usage"].([]any); len(usage) != 1 || usage[0].(map[string]any)["count"] != float64(1) {
		t.Errorf("usage = %v", got["usage"])
	}

	expect(t, f.do(call{method: "DELETE", path: "/api/me/keys/" + id, cookie: cookie}), 204)
	w = f.do(call{method: "GET", path: "/api/session", bearer: token})
	if w.Code != 401 {
		t.Errorf("revoked key: %d %s", w.Code, w.Body)
	}
}
