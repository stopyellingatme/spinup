package auth

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/kit/httpx"
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

// RequireRole guards a handler with the declared roles. It panics for an empty
// list or an undeclared role, so a configuration error fails at startup.
func (a *Auth) RequireRole(roles ...string) func(http.Handler) http.Handler {
	if len(roles) == 0 {
		panic("auth: RequireRole needs at least one role.\nTry: pass a declared role to RequireRole.")
	}
	for _, role := range roles {
		if !a.roles.Valid(role) {
			panic(fmt.Sprintf("auth: RequireRole role %q is not declared. Declared roles: %s.\nTry: pass only declared roles to RequireRole.", role, strings.Join(a.roles.Names, ", ")))
		}
	}
	roles = slices.Clone(roles)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := UserFrom(r.Context())
			if !ok {
				unauthorized(w, r)
				return
			}
			if !user.HasRole(roles...) {
				forbidden(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func forbidden(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusForbidden, Detail: "Your role cannot use this endpoint. Try: sign in with an account that has a required role."})
}
