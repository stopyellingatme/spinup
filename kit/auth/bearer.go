package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/stopyellingatme/spinup/kit/httpx"
)

// Bearer authenticates the token of an Authorization: Bearer header.
type Bearer interface {
	// AuthenticateBearer returns the user and the context for the request.
	// The returned context must retain the values from ctx.
	AuthenticateBearer(ctx context.Context, token string) (context.Context, User, error)
}

// ErrBearerInvalid means the token is unknown, revoked, or expired.
var ErrBearerInvalid = errors.New("auth: invalid bearer token")

// WithBearer sets the authenticator for Bearer tokens. A nil authenticator keeps cookie authentication.
func WithBearer(b Bearer) Option { return func(o *options) { o.bearer = b } }

func (a *Auth) serveBearer(w http.ResponseWriter, r *http.Request, next http.Handler, token string) {
	if strings.HasPrefix(r.URL.Path, "/api/auth/") {
		noStore(w)
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusForbidden, Detail: "An API key cannot call the sign-in routes."})
		return
	}
	if token == "" {
		invalidBearer(w, r)
		return
	}
	ctx, user, err := a.bearer.AuthenticateBearer(r.Context(), token)
	if errors.Is(err, ErrBearerInvalid) {
		invalidBearer(w, r)
		return
	}
	if err != nil {
		a.internalError(w, r, "authenticate the bearer token", err)
		return
	}
	if ctx == nil {
		ctx = r.Context()
	}
	user.Role = a.effectiveRole(user.Role)
	ctx = context.WithValue(ctx, userContextKey, user)
	if a.actorContext != nil {
		ctx = a.actorContext(ctx, user)
	}
	next.ServeHTTP(&guardWriter{ResponseWriter: w}, r.WithContext(ctx))
}

func invalidBearer(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusUnauthorized, Detail: "The API key is not valid."})
}
