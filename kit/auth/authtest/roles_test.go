package authtest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stopyellingatme/spinup/kit/auth"
)

func TestRoleHelpers(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		wrap func(http.Handler) http.Handler
		want auth.User
	}{
		{"As keeps empty role", func(h http.Handler) http.Handler { return As(h, 42) }, auth.User{ID: 42}},
		{"AsRole", func(h http.Handler) http.Handler { return AsRole(h, 42, "owner") }, auth.User{ID: 42, Role: "owner"}},
		{"AsUser", func(h http.Handler) http.Handler {
			return AsUser(h, auth.User{ID: 42, Email: "a@example.com", Role: "front_desk"})
		}, auth.User{ID: 42, Email: "a@example.com", Role: "front_desk"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := tt.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, ok := auth.UserFrom(r.Context())
				if !ok || got != tt.want {
					t.Errorf("UserFrom = %+v, %v, want %+v", got, ok, tt.want)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d", rec.Code)
			}
		})
	}
}
