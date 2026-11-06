package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stopyellingatme/spinup/kit/httpx"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

func TestParseRegistration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		value   string
		want    RegistrationMode
		invalid bool
	}{
		{"", RegistrationOpen, false}, {"open", RegistrationOpen, false},
		{"first-user", RegistrationFirstUser, false}, {"closed", RegistrationClosed, false},
		{"OPEN", "", true}, {" open ", "", true}, {"invalid", "", true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Parallel()
			got, err := ParseRegistration(tc.value)
			if !tc.invalid {
				if err != nil || got != tc.want {
					t.Fatalf("ParseRegistration(%q) = %q, %v", tc.value, got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "AUTH_REGISTRATION: invalid value") || !strings.Contains(err.Error(), "Valid values: open, first-user, closed.") || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("ParseRegistration(%q) error = %v", tc.value, err)
			}
		})
	}
}

func TestRegistrationOptions(t *testing.T) {
	t.Parallel()
	pool, err := pgxpool.New(t.Context(), "postgres://localhost/unused")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, tc := range []struct {
		name string
		opts []Option
		want RegistrationMode
	}{
		{"default", nil, RegistrationOpen},
		{"empty", []Option{WithRegistration("")}, RegistrationOpen},
		{"open", []Option{WithRegistration(RegistrationOpen)}, RegistrationOpen},
		{"first-user", []Option{WithRegistration(RegistrationFirstUser)}, RegistrationFirstUser},
		{"closed", []Option{WithRegistration(RegistrationClosed)}, RegistrationClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := append([]Option{withEnv(nil), func(o *options) { o.params = testParams }}, tc.opts...)
			a, err := New(pool, opts...)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = a.Close(context.Background()) })
			if a.registration != tc.want {
				t.Fatalf("registration = %q, want %q", a.registration, tc.want)
			}
		})
	}
	t.Run("invalid", func(t *testing.T) {
		t.Parallel()
		a, err := New(pool, WithRegistration("invalid"))
		if err == nil {
			_ = a.Close(context.Background())
			t.Fatal("New accepted an invalid registration mode")
		}
		if !strings.Contains(err.Error(), `AUTH_REGISTRATION: invalid value "invalid"`) || !strings.Contains(err.Error(), "\nTry:") {
			t.Fatalf("New error = %v", err)
		}
	})
}

func assertRegistrationClosed(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("registration status = %d: %s", rec.Code, rec.Body)
	}
	problem := testkit.DecodeJSON[httpx.Problem](t, rec)
	if problem.Detail != "Registration is closed. Try: ask an administrator to create your account." {
		t.Fatalf("registration detail = %q", problem.Detail)
	}
	if rec.Header().Get("Content-Type") != "application/problem+json" || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("WWW-Authenticate") != "" || sessionCookie(rec) != nil {
		t.Fatalf("registration headers = %v", rec.Header())
	}
}

func TestRegistrationModes(t *testing.T) {
	t.Parallel()
	for _, roles := range []Roles{DefaultRoles(), {Names: []string{"owner", "cook"}, Admin: "owner", Default: "cook"}} {
		for _, mode := range []RegistrationMode{RegistrationOpen, RegistrationFirstUser, RegistrationClosed} {
			t.Run(roles.Admin+"/"+string(mode), func(t *testing.T) {
				t.Parallel()
				f := newFixture(t, WithRoles(roles), WithRegistration(mode))
				for i := range 2 {
					body := creds(fmt.Sprintf("person%d@example.com", i), goodPassword)
					rec := f.do(t, request{method: http.MethodPost, path: pathRegister, body: body})
					if mode == RegistrationClosed || mode == RegistrationFirstUser && i > 0 {
						assertRegistrationClosed(t, rec)
						continue
					}
					if rec.Code != http.StatusCreated {
						t.Fatalf("register = %d: %s", rec.Code, rec.Body)
					}
					user := testkit.DecodeJSON[userResponse](t, rec).User
					want := roles.Default
					if mode == RegistrationFirstUser {
						want = roles.Admin
					}
					var stored string
					if err := f.pool.QueryRow(t.Context(), `SELECT role FROM users WHERE id=$1`, user.ID).Scan(&stored); err != nil || user.Role != want || stored != want {
						t.Fatalf("user=%+v stored=%q, %v", user, stored, err)
					}
				}
			})
		}
	}
}

func TestParallelFirstUserRegistrations(t *testing.T) {
	t.Parallel()
	f := newFixture(t, WithRoles(Roles{Names: []string{"owner", "cook"}, Admin: "owner", Default: "cook"}), WithRegistration(RegistrationFirstUser), WithHashLimits(20, 20, 10*time.Second))
	results := make(chan *httptest.ResponseRecorder, 20)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			<-start
			results <- f.do(t, request{method: http.MethodPost, path: pathRegister, body: creds(fmt.Sprintf("person%d@example.com", i), goodPassword)})
		})
	}
	close(start)
	wg.Wait()
	close(results)
	created := 0
	for rec := range results {
		if rec.Code == http.StatusCreated {
			created++
			if user := testkit.DecodeJSON[userResponse](t, rec).User; user.Role != f.auth.roles.Admin {
				t.Fatalf("first user = %+v", user)
			}
		} else {
			assertRegistrationClosed(t, rec)
		}
	}
	users, err := f.auth.ListUsers(t.Context())
	if err != nil || created != 1 || len(users) != 1 || users[0].Role != f.auth.roles.Admin {
		t.Fatalf("created=%d users=%+v, %v", created, users, err)
	}
}

func TestCreateUserRegistrationModes(t *testing.T) {
	t.Parallel()
	for _, mode := range []RegistrationMode{RegistrationOpen, RegistrationFirstUser, RegistrationClosed} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, WithRegistration(mode))
			for i := range 2 {
				user, err := f.auth.CreateUser(t.Context(), fmt.Sprintf("trusted%d@example.com", i), goodPassword, f.auth.roles.Default)
				if err != nil || user.Role != f.auth.roles.Default {
					t.Fatalf("trusted CreateUser = %+v, %v", user, err)
				}
			}
			if mode != RegistrationOpen {
				rec := f.do(t, request{method: http.MethodPost, path: pathRegister, body: creds("public@example.com", goodPassword)})
				assertRegistrationClosed(t, rec)
			}
		})
	}
}

func TestOIDCRegistrationRefusal(t *testing.T) {
	t.Parallel()
	p := newOIDCTestProvider(t)
	app := newOIDCTestApp(t, p)
	app.h.user = func(context.Context, string, string, string, bool) (User, int64, error) {
		return User{}, 0, fmt.Errorf("OIDC account: %w", errRegistrationClosed)
	}
	cookie, callback := app.start(t, "/account")
	rec := app.callback(t, cookie, callback)
	assertRegistrationClosed(t, rec)
	if app.linked != 0 {
		t.Fatal("a refused OIDC callback linked an account")
	}
}

func TestRegisterRejectsRole(t *testing.T) {
	t.Parallel()
	a := &Auth{registration: RegistrationOpen}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, pathRegister,
		strings.NewReader(`{"email":"person@example.com","password":"correct horse battery","role":"manager"}`))
	req.Header.Set("Content-Type", "application/json")
	a.register(rec, req)
	if rec.Code != http.StatusBadRequest || sessionCookie(rec) != nil {
		t.Fatalf("register with role = %d: %s", rec.Code, rec.Body)
	}
}

func TestOIDCRegistrationModes(t *testing.T) {
	t.Parallel()
	for _, roles := range []Roles{DefaultRoles(), {Names: []string{"owner", "cook"}, Admin: "owner", Default: "cook"}} {
		for _, mode := range []RegistrationMode{RegistrationOpen, RegistrationFirstUser, RegistrationClosed} {
			t.Run(roles.Admin+"/"+string(mode), func(t *testing.T) {
				t.Parallel()
				f := oidcDBFixture(t, WithRoles(roles), WithRegistration(mode))
				for i := range 2 {
					user, _, err := f.auth.oidcUser(t.Context(), "test", fmt.Sprintf("subject%d", i), fmt.Sprintf("person%d@example.com", i), true)
					if mode == RegistrationClosed || mode == RegistrationFirstUser && i > 0 {
						if !errors.Is(err, errRegistrationClosed) {
							t.Fatalf("OIDC creation = %+v, %v", user, err)
						}
						continue
					}
					want := roles.Default
					if mode == RegistrationFirstUser {
						want = roles.Admin
					}
					var stored string
					if err != nil || user.Role != want {
						t.Fatalf("OIDC creation = %+v, %v", user, err)
					}
					if err := f.pool.QueryRow(t.Context(), `SELECT role FROM users WHERE id=$1`, user.ID).Scan(&stored); err != nil || stored != want {
						t.Fatalf("OIDC stored role = %q, %v", stored, err)
					}
					// A known identity can sign in when creation is closed.
					closed := &Auth{pool: f.pool, roles: roles, registration: RegistrationClosed}
					known, _, err := closed.oidcUser(t.Context(), "test", fmt.Sprintf("subject%d", i), "changed@example.com", true)
					if err != nil || known != user {
						t.Fatalf("known OIDC identity = %+v, %v", known, err)
					}
				}
				users, err := f.auth.ListUsers(t.Context())
				wantCount := 2
				switch mode {
				case RegistrationFirstUser:
					wantCount = 1
				case RegistrationClosed:
					wantCount = 0
				}
				if err != nil || len(users) != wantCount {
					t.Fatalf("OIDC users = %+v, %v", users, err)
				}
			})
		}
	}
}

func TestOIDCLinksExistingAccountWhenRegistrationClosed(t *testing.T) {
	t.Parallel()
	for _, mode := range []RegistrationMode{RegistrationFirstUser, RegistrationClosed} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			f := oidcDBFixture(t, WithRegistration(mode), WithRoles(Roles{Names: []string{"owner", "cook"}, Admin: "owner", Default: "cook"}))
			user, err := f.auth.CreateUser(t.Context(), "person@example.com", goodPassword, f.auth.roles.Default)
			if err != nil {
				t.Fatal(err)
			}
			linked, _, err := f.auth.oidcUser(t.Context(), "test", "subject", user.Email, true)
			if err != nil || linked != user {
				t.Fatalf("existing OIDC account = %+v, %v", linked, err)
			}
		})
	}
}

func TestParallelOIDCAndRegisterFirstUser(t *testing.T) {
	t.Parallel()
	f := oidcDBFixture(t, WithRegistration(RegistrationFirstUser), WithRoles(Roles{Names: []string{"owner", "cook"}, Admin: "owner", Default: "cook"}))
	p := newOIDCTestProvider(t)
	app := newOIDCTestApp(t, p)
	app.h.auth = f.auth
	app.h.user = f.auth.oidcUser
	mux := http.NewServeMux()
	mux.Handle("GET /api/auth/oidc/{provider}/start", f.auth.endpoint(app.h.start))
	mux.Handle("GET /api/auth/oidc/{provider}/callback", f.auth.endpoint(app.h.callback))
	app.handler = f.auth.Middleware()(mux)
	cookie, callback := app.start(t, "/account")
	results := make(chan *httptest.ResponseRecorder, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		<-start
		results <- f.do(t, request{method: http.MethodPost, path: pathRegister, body: creds("registered@example.com", goodPassword)})
	})
	wg.Go(func() {
		<-start
		results <- app.callback(t, cookie, callback)
	})
	close(start)
	wg.Wait()
	close(results)
	succeeded := 0
	for rec := range results {
		if rec.Code == http.StatusCreated || rec.Code == http.StatusSeeOther {
			succeeded++
		} else {
			assertRegistrationClosed(t, rec)
		}
	}
	users, err := f.auth.ListUsers(t.Context())
	if err != nil || succeeded != 1 || len(users) != 1 || users[0].Role != f.auth.roles.Admin {
		t.Fatalf("successes=%d users=%+v, %v", succeeded, users, err)
	}
}

func TestClosedRegistrationRefusesBeforeReadingCredentials(t *testing.T) {
	t.Parallel()
	a := &Auth{registration: RegistrationClosed}
	for _, body := range []string{"", "invalid JSON", `{"email":"person@example.com","password":"correct horse battery"}`} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, pathRegister, strings.NewReader(body))
			rec := httptest.NewRecorder()
			a.register(rec, req)
			assertRegistrationClosed(t, rec)
		})
	}
}
