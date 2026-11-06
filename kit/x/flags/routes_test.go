package flags

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/auth"
)

func routeFixture(t *testing.T, s *Store, f *Flags) http.Handler {
	t.Helper()
	a, err := auth.New(s.pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(t.Context()) })
	mux := http.NewServeMux()
	s.Routes(a)(mux)
	return f.Middleware()(mux)
}

func routeRequest(h http.Handler, method, path, body, role string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if role != "" {
		r = r.WithContext(auth.WithUser(r.Context(), auth.User{ID: 1, Role: role}))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRoutesPublicAndGuards(t *testing.T) {
	pool := lazyStorePool(t)
	declared := Declared{"public": {Public: true}, "private": {Default: true}}
	f, err := New(WithDeclared(declared))
	if err != nil {
		t.Fatal(err)
	}
	f.Replace(NewSnapshot(declared, map[string]Rule{"public": {Roles: []string{"staff"}}}, time.Now()))
	h := routeFixture(t, NewStore(pool, f, auth.DefaultRoles()), f)
	for _, tc := range []struct{ role, body string }{{"", `{"flags":{"public":false}}`}, {"staff", `{"flags":{"public":true}}`}} {
		w := routeRequest(h, "GET", "/api/flags", "", tc.role)
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != tc.body || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("public response: %d %s %v", w.Code, w.Body, w.Header())
		}
	}
	for _, tc := range []struct {
		role   string
		status int
	}{{"", 401}, {"staff", 403}} {
		w := routeRequest(h, "GET", "/api/admin/flags", "", tc.role)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("guard response: %d %s", w.Code, w.Body)
		}
	}
}

func TestRoutesValidation(t *testing.T) {
	pool := lazyStorePool(t)
	s, f, _ := storeFixture(t, pool)
	h := routeFixture(t, s, f)
	for _, tc := range []struct {
		path, body string
		status     int
		contains   string
	}{
		{"/api/admin/flags/missing", `{"enabled":true,"roles":[],"percentage":0}`, 404, `not declared`},
		{"/api/admin/flags/new_checkout", `{"enabled":true,"roles":["bad"],"percentage":0}`, 422, `role`},
		{"/api/admin/flags/new_checkout", `{"enabled":true,"roles":[],"percentage":101}`, 422, `percentage`},
		{"/api/admin/flags/new_checkout", `{"enabled":true}`, 422, `required`},
		{"/api/admin/flags/new_checkout", `{"enabled":true,"roles":[],"percentage":0,"extra":1}`, 400, `valid JSON`},
		{"/api/admin/flags/new_checkout", `{"enabled":true,"enabled":false,"roles":[],"percentage":0}`, 400, `valid JSON`},
	} {
		w := routeRequest(h, "PUT", tc.path, tc.body, "manager")
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) {
			t.Errorf("%s: %d %s", tc.body, w.Code, w.Body)
		}
	}
}

func TestRoutesDatabaseWrites(t *testing.T) {
	pool := flagDB(t, false)
	s, f, _ := storeFixture(t, pool)
	if _, err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	storeExec(t, pool, `INSERT INTO users (email, password_hash) VALUES ('target@example.com', 'hash')`)
	h := routeFixture(t, s, f)
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"PUT", "/api/admin/flags/new_checkout", `{"enabled":true,"roles":["staff"],"percentage":50}`, 200},
		{"PUT", "/api/admin/flags/new_checkout/users/target%40example.com", `{"enabled":false}`, 200},
		{"DELETE", "/api/admin/flags/new_checkout/users/target%40example.com", "", 204},
		{"PUT", "/api/admin/flags/new_checkout/users/missing%40example.com", `{"enabled":true}`, 422},
		{"GET", "/api/admin/flags", "", 200},
	} {
		w := routeRequest(h, tc.method, tc.path, tc.body, "manager")
		if w.Code != tc.status {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
	}
}
