package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stopyellingatme/spinup/kit/testkit"
)

func TestUserRoleValidation(t *testing.T) {
	t.Parallel()
	a := &Auth{roles: Roles{Names: []string{"owner", "cook"}, Admin: "owner", Default: "cook"}}
	for _, action := range []string{"create", "set-role"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			var err error
			if action == "create" {
				_, err = a.CreateUser(t.Context(), "a@example.com", goodPassword, "unknown")
			} else {
				err = a.SetRole(t.Context(), "a@example.com", "unknown")
			}
			if !errors.Is(err, ErrUnknownRole) || !strings.Contains(err.Error(), `role "unknown" is not declared. Declared roles: owner, cook.`) || !strings.Contains(err.Error(), "\nTry:") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestUserStoreRoles(t *testing.T) {
	t.Parallel()
	for _, roles := range []Roles{DefaultRoles(), {Names: []string{"owner", "cook"}, Admin: "owner", Default: "cook"}} {
		t.Run(roles.Admin, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, WithRoles(roles))
			ctx := t.Context()
			admin, err := f.auth.CreateUser(ctx, "  ADMIN@Example.com ", goodPassword, roles.Admin)
			if err != nil || admin.Email != "admin@example.com" || admin.Role != roles.Admin {
				t.Fatalf("CreateUser = %+v, %v", admin, err)
			}
			for _, tc := range []struct {
				email, role string
				want        error
			}{
				{"admin@example.com", "missing", ErrUnknownRole},
				{"missing@example.com", roles.Default, ErrUserNotFound},
				{"admin@example.com", roles.Default, ErrLastAdmin},
			} {
				if err := f.auth.SetRole(ctx, tc.email, tc.role); !errors.Is(err, tc.want) || !strings.Contains(err.Error(), "\nTry:") {
					t.Fatalf("SetRole(%q, %q) = %v, want %v", tc.email, tc.role, err, tc.want)
				}
			}
			cookie := f.register(t, "cook@example.com")
			if err := f.auth.SetRole(ctx, " COOK@Example.com ", roles.Admin); err != nil {
				t.Fatal(err)
			}
			rec := f.do(t, request{method: http.MethodGet, path: "/api/session", cookie: cookie})
			if got := testkit.DecodeJSON[userResponse](t, rec); got.User.Role != roles.Admin {
				t.Fatalf("session = %+v", got)
			}
			if err := f.auth.SetRole(ctx, admin.Email, roles.Default); err != nil {
				t.Fatal(err)
			}
			users, err := f.auth.ListUsers(ctx)
			if err != nil || len(users) != 2 || users[0].Email != admin.Email || users[0].Role != roles.Default || users[1].Role != roles.Admin {
				t.Fatalf("ListUsers = %+v, %v", users, err)
			}
		})
	}
}

func TestSetRoleZeroAdministrators(t *testing.T) {
	t.Parallel()
	roles := Roles{Names: []string{"owner", "cook"}, Admin: "owner", Default: "cook"}
	f := newFixture(t, WithRoles(roles))
	for _, stored := range []string{"", "retired", "cook"} {
		email := fmt.Sprintf("account%d@example.com", len(stored))
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO users(email, password_hash, role) VALUES ($1, 'unused', $2)`, email, stored); err != nil {
			t.Fatal(err)
		}
		if err := f.auth.SetRole(t.Context(), email, "cook"); err != nil {
			t.Fatalf("stored=%q: %v", stored, err)
		}
	}
}

func TestParallelAdminDemotions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, email := range []string{"a@example.com", "b@example.com"} {
		if _, err := f.auth.CreateUser(t.Context(), email, goodPassword, f.auth.roles.Admin); err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, email := range []string{"a@example.com", "b@example.com"} {
		wg.Go(func() { results <- f.auth.SetRole(t.Context(), email, f.auth.roles.Default) })
	}
	wg.Wait()
	close(results)
	var succeeded, refused int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrLastAdmin):
			refused++
		default:
			t.Fatal(err)
		}
	}
	if succeeded != 1 || refused != 1 {
		t.Fatalf("succeeded=%d refused=%d", succeeded, refused)
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM users WHERE role = $1`, f.auth.roles.Admin).Scan(&count); err != nil || count != 1 {
		t.Fatalf("admins=%d, %v", count, err)
	}
}

func TestBackfillRoles(t *testing.T) {
	t.Parallel()
	for _, adminDefault := range []bool{false, true} {
		t.Run(fmt.Sprint(adminDefault), func(t *testing.T) {
			t.Parallel()
			roles := Roles{Names: []string{"owner", "cook"}, Admin: "owner", Default: "cook"}
			if adminDefault {
				roles.Default = roles.Admin
			}
			f := newFixture(t, WithRoles(roles))
			if _, err := f.pool.Exec(t.Context(), `INSERT INTO users(email,password_hash,role) VALUES ('old@example.com','unused',''), ('retired@example.com','unused','retired')`); err != nil {
				t.Fatal(err)
			}
			var id int64
			if err := f.pool.QueryRow(t.Context(), `SELECT id FROM users WHERE email='old@example.com'`).Scan(&id); err != nil {
				t.Fatal(err)
			}
			u, found, err := f.auth.userByID(t.Context(), id)
			if err != nil || !found {
				t.Fatal(err)
			}
			want := roles.Default
			if adminDefault {
				want = ""
			}
			if u.Role != want || u.HasRole(roles.Admin) {
				t.Fatalf("effective legacy user=%+v", u)
			}
			n, err := f.auth.BackfillRoles(t.Context())
			if adminDefault {
				if err == nil || !strings.Contains(err.Error(), "default_role equals admin_role") || !strings.Contains(err.Error(), "\nTry:") || n != 0 {
					t.Fatalf("BackfillRoles=%d, %v", n, err)
				}
			} else if err != nil || n != 1 {
				t.Fatalf("BackfillRoles=%d, %v", n, err)
			}
			users, err := f.auth.ListUsers(t.Context())
			if err != nil || len(users) != 2 {
				t.Fatalf("ListUsers=%+v, %v", users, err)
			}
			for _, user := range users {
				if user.Email == "retired@example.com" && (user.Role != "retired" || user.HasRole(roles.Names...)) {
					t.Fatalf("undeclared user=%+v", user)
				}
			}
			if !adminDefault {
				if n, err := f.auth.BackfillRoles(t.Context()); err != nil || n != 0 {
					t.Fatalf("second backfill=%d, %v", n, err)
				}
			}
		})
	}
}

func TestCreateUserInputValidation(t *testing.T) {
	t.Parallel()
	a := &Auth{roles: DefaultRoles()}
	for _, tc := range []struct{ name, email, password, want string }{
		{"email", "invalid", goodPassword, "email address is invalid"},
		{"short password", "user@example.com", "short", "password must have"},
		{"long password", "user@example.com", strings.Repeat("x", MaxPasswordLength+1), "password must have"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := a.CreateUser(t.Context(), tc.email, tc.password, a.roles.Default)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "\nTry:") || strings.Contains(err.Error(), tc.password) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCreateUserDuplicateIsPrivate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	const email = "existing@example.com"
	if _, err := f.auth.CreateUser(t.Context(), email, goodPassword, f.auth.roles.Default); err != nil {
		t.Fatal(err)
	}
	_, err := f.auth.CreateUser(t.Context(), email, goodPassword, f.auth.roles.Default)
	if err == nil || strings.Contains(err.Error(), email) || strings.Contains(err.Error(), "already") || strings.Contains(err.Error(), "duplicate") || !strings.Contains(err.Error(), "\nTry:") {
		t.Fatalf("error=%v", err)
	}
	users, err := f.auth.ListUsers(t.Context())
	if err != nil || len(users) != 1 {
		t.Fatalf("ListUsers=%+v, %v", users, err)
	}
}

func TestEmptyRolesNeverCountAsAdministrators(t *testing.T) {
	t.Parallel()
	roles := Roles{Names: []string{"owner", "cook"}, Admin: "owner", Default: "owner"}
	f := newFixture(t, WithRoles(roles))
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO users(email,password_hash,role) VALUES ('legacy@example.com','unused','')`); err != nil {
		t.Fatal(err)
	}
	admin, err := f.auth.CreateUser(t.Context(), "admin@example.com", goodPassword, roles.Admin)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.auth.SetRole(t.Context(), admin.Email, "cook"); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demotion with empty legacy role = %v", err)
	}
	if err := f.auth.SetRole(t.Context(), "legacy@example.com", "cook"); err != nil {
		t.Fatalf("assigning an empty legacy role = %v", err)
	}
}
