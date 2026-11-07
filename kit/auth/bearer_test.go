package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/kit/testkit"
)

type fakeBearer func(context.Context, string) (context.Context, User, error)

func (f fakeBearer) AuthenticateBearer(ctx context.Context, token string) (context.Context, User, error) {
	return f(ctx, token)
}

type bearerTestKey struct{}
type bearerActorKey struct{}

// newBearerFixture uses a closed pool so a session lookup cannot reach a socket.
func newBearerFixture(t *testing.T, opts ...Option) *fixture {
	t.Helper()
	pool := lazyPool(t)
	pool.Close()
	base := []Option{withEnv(map[string]string{"AUTH_RATE_LIMIT": "off"}), func(o *options) {
		o.cleanup, o.sweep = 0, 0
	}}
	a, err := New(pool, append(base, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	mux := http.NewServeMux()
	a.Routes(mux)
	return &fixture{auth: a, handler: a.Middleware()(mux)}
}

func TestBearerSuccess(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, scheme, cookie, method, storedRole, defaultRole, wantRole string
	}{
		{"default role", "Bearer", "", http.MethodGet, "", "staff", "staff"},
		{"stored role", "Bearer", "", http.MethodGet, "manager", "staff", "manager"},
		{"removed role", "Bearer", "", http.MethodGet, "removed", "staff", "removed"},
		{"no implicit admin", "Bearer", "", http.MethodGet, "", "manager", ""},
		{"lowercase scheme", "bearer", "", http.MethodGet, "staff", "staff", "staff"},
		{"mixed case scheme", "bEaReR", "", http.MethodGet, "staff", "staff", "staff"},
		{"bearer wins over cookie", "Bearer", "unread-session", http.MethodGet, "staff", "staff", "staff"},
		{"cross-site unsafe request", "Bearer", "", http.MethodPost, "staff", "staff", "staff"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want := User{ID: 42, Email: "key@example.com", Role: tc.wantRole}
			authCalls, actorCalls, nextCalls := 0, 0, 0
			f := newBearerFixture(t,
				WithRoles(Roles{Names: []string{"manager", "staff"}, Admin: "manager", Default: tc.defaultRole}),
				WithBearer(fakeBearer(func(ctx context.Context, token string) (context.Context, User, error) {
					authCalls++
					if token != "key_secret" {
						t.Errorf("token = %q, want key_secret", token)
					}
					if ctx.Value(installedContextKey) == nil {
						t.Error("authenticator context has no installed marker")
					}
					user := want
					user.Role = tc.storedRole
					return context.WithValue(ctx, bearerTestKey{}, "key metadata"), user, nil
				})),
				WithActorContext(func(ctx context.Context, user User) context.Context {
					actorCalls++
					if got, ok := UserFrom(ctx); !ok || got != want || user != want {
						t.Errorf("actor user = %+v, context user = %+v (%v), want %+v", user, got, ok, want)
					}
					if ctx.Value(bearerTestKey{}) != "key metadata" {
						t.Error("actor context lost the authenticator metadata")
					}
					return context.WithValue(ctx, bearerActorKey{}, user.ID)
				}),
			)
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalls++
				if got, ok := UserFrom(r.Context()); !ok || got != want {
					t.Errorf("user = %+v (%v), want %+v", got, ok, want)
				}
				if r.Context().Value(bearerTestKey{}) != "key metadata" || r.Context().Value(bearerActorKey{}) != want.ID {
					t.Error("handler context lost the authenticator metadata or actor")
				}
				if _, ok := w.(*guardWriter); !ok {
					t.Errorf("writer = %T, want *guardWriter", w)
				}
				w.WriteHeader(http.StatusNoContent)
			})
			guarded := Required(next)
			if tc.wantRole != "" && tc.wantRole != "removed" {
				guarded = f.auth.RequireRole(tc.wantRole)(guarded)
			}
			f.handler = f.auth.Middleware()(f.auth.RequireUser(guarded))
			rec := f.do(t, request{method: tc.method, path: "/api/private", cookie: tc.cookie, headers: map[string]string{
				"Authorization": tc.scheme + " key_secret", "Sec-Fetch-Site": "cross-site", "Origin": "https://other.test",
			}})
			if rec.Code != http.StatusNoContent || authCalls != 1 || actorCalls != 1 || nextCalls != 1 {
				t.Fatalf("status = %d, calls = %d/%d/%d, want 204 and 1/1/1: %s", rec.Code, authCalls, actorCalls, nextCalls, rec.Body)
			}
			if cookies := rec.Header().Values("Set-Cookie"); len(cookies) != 0 {
				t.Errorf("Set-Cookie = %v, want none", cookies)
			}
			if got := rec.Header().Get("Content-Security-Policy"); got != DefaultCSP {
				t.Errorf("CSP = %q, want %q", got, DefaultCSP)
			}
		})
	}
}

func TestBearerSession(t *testing.T) {
	t.Parallel()
	want := User{ID: 42, Email: "key@example.com", Role: "staff"}
	f := newBearerFixture(t, WithBearer(fakeBearer(func(ctx context.Context, _ string) (context.Context, User, error) {
		return ctx, want, nil
	})))
	rec := f.do(t, request{method: http.MethodGet, path: "/api/session", headers: map[string]string{"Authorization": "Bearer key_secret"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := testkit.DecodeJSON[userResponse](t, rec); got.User != want {
		t.Errorf("user = %+v, want %+v", got.User, want)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || len(rec.Header().Values("Set-Cookie")) != 0 {
		t.Errorf("headers = %v, want no-store and no cookie", rec.Header())
	}
}

func TestBearerErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path, header string
		err                error
		status, calls      int
		detail             string
	}{
		{"empty token", "/api/session", "Bearer ", nil, 401, 0, "The API key is not valid."},
		{"bare scheme", "/api/session", "Bearer", nil, 401, 0, "The API key is not valid."},
		{"invalid token", "/api/session", "Bearer key_secret", ErrBearerInvalid, 401, 1, "The API key is not valid."},
		{"wrapped invalid token", "/api/session", "Bearer key_secret", fmt.Errorf("lookup: %w", ErrBearerInvalid), 401, 1, "The API key is not valid."},
		{"store failure", "/api/session", "Bearer key_secret", errors.New("store unavailable"), 500, 1, ""},
		{"login", "/api/auth/login", "Bearer key_secret", nil, 403, 0, "An API key cannot call the sign-in routes."},
		{"register", "/api/auth/register", "Bearer key_secret", nil, 403, 0, "An API key cannot call the sign-in routes."},
		{"logout", "/api/auth/logout", "Bearer key_secret", nil, 403, 0, "An API key cannot call the sign-in routes."},
		{"oidc", "/api/auth/oidc/provider/start", "Bearer key_secret", nil, 403, 0, "An API key cannot call the sign-in routes."},
		{"sign-in before empty token", "/api/auth/login", "Bearer ", nil, 403, 0, "An API key cannot call the sign-in routes."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			var logs bytes.Buffer
			f := newBearerFixture(t, WithLogger(slog.New(slog.NewTextHandler(&logs, nil))),
				WithBearer(fakeBearer(func(ctx context.Context, _ string) (context.Context, User, error) {
					calls++
					return ctx, User{}, tc.err
				})))
			f.handler = f.auth.Middleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("a rejected request reached the handler")
			}))
			rec := f.do(t, request{method: http.MethodPost, path: tc.path, cookie: "unread-session", headers: map[string]string{
				"Authorization": tc.header, "Sec-Fetch-Site": "cross-site",
			}})
			if rec.Code != tc.status || calls != tc.calls {
				t.Fatalf("status = %d, calls = %d, want %d and %d: %s", rec.Code, calls, tc.status, tc.calls, rec.Body)
			}
			problem := problemOf(t, rec)
			if problem.Status != tc.status || problem.Detail != tc.detail {
				t.Errorf("problem = %+v, want status %d and detail %q", problem, tc.status, tc.detail)
			}
			wantChallenge := ""
			if tc.status == http.StatusUnauthorized {
				wantChallenge = `Bearer error="invalid_token"`
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != wantChallenge {
				t.Errorf("WWW-Authenticate = %q, want %q", got, wantChallenge)
			}
			if rec.Header().Get("Cache-Control") != "no-store" || len(rec.Header().Values("Set-Cookie")) != 0 {
				t.Errorf("headers = %v, want no-store and no cookie", rec.Header())
			}
			if tc.status == http.StatusInternalServerError && !strings.Contains(logs.String(), "auth: cannot authenticate the bearer token") {
				t.Errorf("log = %s, want the bearer authentication action", &logs)
			}
		})
	}
}

func TestBearerCookieFallback(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, header string
		configured   bool
	}{
		{"header ignored without WithBearer", "Bearer key_secret", false},
		{"Basic scheme", "Basic credentials", true},
		{"no header", "", true},
		{"different scheme", "BearerOther key_secret", true},
		{"tab separator", "Bearer\tkey_secret", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var opts []Option
			if tc.configured {
				opts = append(opts, WithBearer(fakeBearer(func(ctx context.Context, _ string) (context.Context, User, error) {
					t.Error("the cookie branch called the Bearer authenticator")
					return ctx, User{}, ErrBearerInvalid
				})))
			}
			f := newBearerFixture(t, opts...)
			// Get checks that LoadAndSave installed a session context.
			f.handler = f.auth.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := f.auth.sessions.Get(r.Context(), sessionUserKey); got != nil {
					t.Errorf("session user = %v, want none", got)
				}
				f.auth.RequireUser(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					t.Error("an anonymous request reached the protected handler")
				})).ServeHTTP(w, r)
			}))
			headers := map[string]string{"Authorization": tc.header, "Sec-Fetch-Site": "cross-site"}
			rec := f.do(t, request{method: http.MethodGet, path: "/api/private", headers: headers})
			if rec.Code != http.StatusUnauthorized || problemOf(t, rec).Detail != "No user is signed in." {
				t.Fatalf("GET status = %d, want cookie authentication: %s", rec.Code, rec.Body)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != "" {
				t.Errorf("cookie challenge = %q, want none", got)
			}
			rec = f.do(t, request{method: http.MethodPost, path: "/api/private", headers: headers})
			if rec.Code != http.StatusForbidden || problemOf(t, rec).Detail != "Cross-origin requests that change state are not allowed." {
				t.Fatalf("POST status = %d, want cookie CSRF protection: %s", rec.Code, rec.Body)
			}
		})
	}
}
