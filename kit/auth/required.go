package auth

import (
	"context"
	"net/http"
)

// Required answers 401 when the request has no signed-in user. Otherwise it
// calls next. Use it for routes that require a user after auth middleware runs.
func Required(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := UserFrom(r.Context()); !ok {
			unauthorized(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// WithUser stores u in ctx. Use WithUser only in tests and trusted middleware.
func WithUser(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, userContextKey, u)
}
