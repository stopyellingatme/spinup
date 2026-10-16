package authtest

import (
	"net/http"

	"github.com/stopyellingatme/spinup/kit/auth"
)

// As serves h with userID as the signed-in user.
func As(h http.Handler, userID int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), auth.User{ID: userID})))
	})
}
