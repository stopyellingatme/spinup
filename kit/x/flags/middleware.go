package flags

import (
	"net/http"

	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/httpx"
)

// Middleware installs one snapshot and the subject from kit auth in each request.
// Register it after the auth middleware.
func (f *Flags) Middleware() httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			snap := f.Snapshot()
			subject := Subject{Anonymous: true}
			if u, ok := auth.UserFrom(r.Context()); ok {
				subject = Subject{UserID: u.ID, Role: u.Role}
			}
			next.ServeHTTP(w, r.WithContext(f.scope(r.Context(), snap, subject)))
		})
	}
}
