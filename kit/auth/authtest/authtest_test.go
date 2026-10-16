package authtest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stopyellingatme/spinup/kit/auth"
)

func TestAs(t *testing.T) {
	t.Parallel()

	handler := As(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.UserFrom(r.Context())
		if !ok {
			t.Fatal("UserFrom returned no user")
		}
		if user.ID != 42 {
			t.Fatalf("user ID = %d, want 42", user.ID)
		}
		w.WriteHeader(http.StatusNoContent)
	}), 42)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}
