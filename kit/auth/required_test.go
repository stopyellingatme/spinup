package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stopyellingatme/spinup/kit/httpx"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

func TestRequired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		user       *User
		wantStatus int
		wantBody   string
	}{
		{
			name:       "without user",
			wantStatus: http.StatusUnauthorized,
			wantBody:   "No user is signed in.",
		},
		{
			name:       "with user",
			user:       &User{ID: 42},
			wantStatus: http.StatusNoContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler := Required(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			if tt.user != nil {
				req = req.WithContext(WithUser(req.Context(), *tt.user))
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.user != nil {
				return
			}
			if contentType := rec.Header().Get("Content-Type"); contentType != "application/problem+json" {
				t.Fatalf("Content-Type = %q, want application/problem+json", contentType)
			}
			if cacheControl := rec.Header().Get("Cache-Control"); cacheControl != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", cacheControl)
			}
			problem := testkit.DecodeJSON[httpx.Problem](t, rec)
			if problem.Status != http.StatusUnauthorized || problem.Detail != tt.wantBody {
				t.Fatalf("problem = %+v, want status %d and detail %q", problem, http.StatusUnauthorized, tt.wantBody)
			}
		})
	}
}

func TestWithUser(t *testing.T) {
	t.Parallel()

	want := User{ID: 42, Email: "user@example.com"}
	got, ok := UserFrom(WithUser(t.Context(), want))
	if !ok {
		t.Fatal("UserFrom returned no user")
	}
	if got != want {
		t.Fatalf("UserFrom = %+v, want %+v", got, want)
	}
}

func TestRequireRole(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		user *User
		want int
	}{
		{"no user", nil, http.StatusUnauthorized},
		{"wrong role", &User{ID: 1, Role: "front_desk"}, http.StatusForbidden},
		{"empty role", &User{ID: 1}, http.StatusForbidden},
		{"undeclared stored role", &User{ID: 1, Role: "removed"}, http.StatusForbidden},
		{"right role", &User{ID: 1, Role: "owner"}, http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := &Auth{roles: Roles{[]string{"owner", "front_desk"}, "owner", "front_desk"}}
			called := false
			h := a.RequireRole("owner")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(http.StatusOK) }))
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			if tt.user != nil {
				req = req.WithContext(WithUser(req.Context(), *tt.user))
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want || called != (tt.want == http.StatusOK) {
				t.Fatalf("status = %d, called = %v, want %d", rec.Code, called, tt.want)
			}
			if tt.want == http.StatusOK {
				return
			}
			if rec.Header().Get("Content-Type") != "application/problem+json" || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("WWW-Authenticate") != "" {
				t.Fatalf("headers = %v", rec.Header())
			}
			problem := testkit.DecodeJSON[httpx.Problem](t, rec)
			if problem.Status != tt.want {
				t.Fatalf("problem = %+v", problem)
			}
			if tt.want == http.StatusForbidden && problem.Detail != "Your role cannot use this endpoint. Try: sign in with an account that has a required role." {
				t.Fatalf("detail = %q", problem.Detail)
			}
		})
	}
}

func TestRequireRoleConstruction(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		roles []string
		panic bool
	}{
		{"empty", nil, true},
		{"empty name", []string{""}, true},
		{"undeclared", []string{"manager"}, true},
		{"mixed", []string{"owner", "manager"}, true},
		{"one", []string{"owner"}, false},
		{"several", []string{"owner", "front_desk"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if got := recover(); (got != nil) != tt.panic {
					t.Errorf("panic = %v, want panic %v", got, tt.panic)
				}
			}()
			a := &Auth{roles: Roles{[]string{"owner", "front_desk"}, "owner", "front_desk"}}
			a.RequireRole(tt.roles...)
		})
	}
}
