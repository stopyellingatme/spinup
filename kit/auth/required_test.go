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
