package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/kit/testkit"
)

func TestEffectiveRole(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, stored, admin, fallback, want string
	}{
		{"empty", "", "owner", "cook", "cook"},
		{"explicit", "owner", "owner", "cook", "owner"},
		{"undeclared", "retired", "owner", "cook", "retired"},
		{"empty with administrator default", "", "owner", "owner", ""},
		{"explicit administrator default", "owner", "owner", "owner", "owner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := &Auth{roles: Roles{Names: []string{"owner", "cook"}, Admin: tc.admin, Default: tc.fallback}}
			if got := a.effectiveRole(tc.stored); got != tc.want {
				t.Errorf("effectiveRole(%q) = %q, want %q", tc.stored, got, tc.want)
			}
		})
	}
}

func TestSessionIncludesRole(t *testing.T) {
	t.Parallel()
	a := &Auth{}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/session", nil)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, User{ID: 7, Email: "cook@example.com", Role: "cook"}))
	rec := httptest.NewRecorder()
	a.session(rec, req)
	if got := testkit.DecodeJSON[userResponse](t, rec); got.User.Role != "cook" || !strings.Contains(rec.Body.String(), `"role":"cook"`) {
		t.Fatalf("session = %s", rec.Body.String())
	}
}

func TestRoleStoreReads(t *testing.T) {
	t.Parallel()
	for _, adminDefault := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "administrator default"}[adminDefault], func(t *testing.T) {
			t.Parallel()
			roles := Roles{Names: []string{"owner", "cook"}, Admin: "owner", Default: "cook"}
			if adminDefault {
				roles.Default = roles.Admin
			}
			f := newFixture(t, WithRoles(roles))
			for _, stored := range []string{"", "cook", "owner", "retired"} {
				email := "user" + stored + "@example.com"
				var id int64
				if err := f.pool.QueryRow(t.Context(), `INSERT INTO users(email, password_hash, role) VALUES ($1, 'test hash', $2) RETURNING id`, email, stored).Scan(&id); err != nil {
					t.Fatal(err)
				}
				want := stored
				if stored == "" && !adminDefault {
					want = roles.Default
				}
				user, found, err := f.auth.userByID(t.Context(), id)
				if err != nil || !found || user.Role != want {
					t.Fatalf("userByID: user=%+v found=%v error=%v", user, found, err)
				}
				credential, hash, found, err := f.auth.credentialsByEmail(t.Context(), email)
				if err != nil || !found || credential != user || hash != "test hash" {
					t.Fatalf("credentialsByEmail: user=%+v hash=%q found=%v error=%v", credential, hash, found, err)
				}
			}
		})
	}
}

func TestOIDCCustomRoles(t *testing.T) {
	t.Parallel()
	for _, roles := range []Roles{DefaultRoles(), {Names: []string{"owner", "cook"}, Admin: "owner", Default: "cook"}} {
		t.Run(roles.Admin, func(t *testing.T) {
			t.Parallel()
			f := oidcDBFixture(t, WithRoles(roles))
			user, err := f.auth.oidcUser(t.Context(), "test", "subject", "person@example.com", true)
			if err != nil || user.Role != roles.Default {
				t.Fatalf("create=%+v, %v", user, err)
			}
			var stored string
			if err := f.pool.QueryRow(t.Context(), `SELECT role FROM users WHERE id=$1`, user.ID).Scan(&stored); err != nil || stored != roles.Default {
				t.Fatalf("stored role=%q, %v", stored, err)
			}
			if err := f.auth.SetRole(t.Context(), user.Email, roles.Admin); err != nil {
				t.Fatal(err)
			}
			// The identity and users tables both have email columns.
			joined, err := f.auth.oidcUser(t.Context(), "test", "subject", "changed@example.com", true)
			if err != nil || joined.ID != user.ID || joined.Email != user.Email || joined.Role != roles.Admin {
				t.Fatalf("join=%+v, %v", joined, err)
			}
		})
	}
}

func TestOIDCJoinEffectiveRole(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, stored, fallback, want string
	}{
		{"empty", "", "cook", "cook"},
		{"empty with administrator default", "", "owner", ""},
		{"undeclared", "retired", "cook", "retired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			roles := Roles{Names: []string{"owner", "cook"}, Admin: "owner", Default: tc.fallback}
			f := oidcDBFixture(t, WithRoles(roles))
			user, err := f.auth.oidcUser(t.Context(), "test", "subject", "person@example.com", true)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(t.Context(), `UPDATE users SET role=$1 WHERE id=$2`, tc.stored, user.ID); err != nil {
				t.Fatal(err)
			}
			joined, err := f.auth.oidcUser(t.Context(), "test", "subject", "changed@example.com", true)
			if err != nil || joined.ID != user.ID || joined.Email != user.Email || joined.Role != tc.want {
				t.Fatalf("joined user = %+v, %v", joined, err)
			}
			if tc.stored == "" && tc.fallback == roles.Admin && joined.HasRole(roles.Names...) {
				t.Fatalf("empty role granted access: %+v", joined)
			}
		})
	}
}
