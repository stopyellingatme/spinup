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

// AsRole serves h with userID and role as the signed-in user.
func AsRole(h http.Handler, userID int64, role string) http.Handler {
	return AsUser(h, auth.User{ID: userID, Role: role})
}

// AsUser serves h with u as the signed-in user.
func AsUser(h http.Handler, u auth.User) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u)))
	})
}
