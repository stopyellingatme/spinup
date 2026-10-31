package auth

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRolesValidate(t *testing.T) {
	t.Parallel()
	many := make([]string, 17)
	for i := range many {
		many[i] = fmt.Sprintf("role%d", i)
	}
	tests := []struct {
		name        string
		roles       Roles
		rule, value string
	}{
		{"defaults", DefaultRoles(), "", ""},
		{"single", Roles{[]string{"owner"}, "owner", "owner"}, "", ""},
		{"sixteen", Roles{many[:16], "role0", "role15"}, "", ""},
		{"length boundary", Roles{[]string{strings.Repeat("a", 32)}, strings.Repeat("a", 32), strings.Repeat("a", 32)}, "", ""},
		{"empty list", Roles{}, "1 to 16", "0"},
		{"large list", Roles{many, "role0", "role1"}, "1 to 16", "17"},
		{"duplicate", Roles{[]string{"owner", "owner"}, "owner", "owner"}, "unique", "owner"},
		{"empty name", Roles{[]string{""}, "", ""}, "must match", `""`},
		{"uppercase", Roles{[]string{"Owner"}, "Owner", "Owner"}, "must match", "Owner"},
		{"leading digit", Roles{[]string{"1a"}, "1a", "1a"}, "must match", "1a"},
		{"hyphen", Roles{[]string{"front-desk"}, "front-desk", "front-desk"}, "must match", "front-desk"},
		{"unicode", Roles{[]string{"café"}, "café", "café"}, "must match", "café"},
		{"too long", Roles{[]string{strings.Repeat("a", 33)}, "a", "a"}, "must match", strings.Repeat("a", 33)},
		{"double underscore", Roles{[]string{"a_b", "a__b"}, "a_b", "a_b"}, "double underscore", "a__b"},
		{"trailing underscore", Roles{[]string{"a_", "a"}, "a", "a"}, "trailing underscore", "a_"},
		{"constant collision", Roles{[]string{"a1", "a_1"}, "a1", "a1"}, "RoleConst", "A1"},
		{"reserved constant", Roles{[]string{"set"}, "set", "set"}, "Set", "set"},
		{"missing admin", Roles{[]string{"owner"}, "missing", "owner"}, "admin_role", "missing"},
		{"missing default", Roles{[]string{"owner"}, "owner", "missing"}, "default_role", "missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.roles.Validate()
			if tt.rule == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate accepted invalid roles")
			}
			for _, want := range []string{tt.rule, tt.value, "\nTry:"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
		})
	}
}

func TestRolesValid(t *testing.T) {
	t.Parallel()
	r := Roles{Names: []string{"owner", "front_desk"}, Admin: "owner", Default: "front_desk"}
	for _, tt := range []struct {
		name string
		want bool
	}{{"owner", true}, {"front_desk", true}, {"manager", false}, {"", false}} {
		if got := r.Valid(tt.name); got != tt.want {
			t.Errorf("Valid(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestHasRole(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, role string
		names      []string
		want       bool
	}{
		{"match", "owner", []string{"owner"}, true},
		{"one of several", "front_desk", []string{"owner", "front_desk"}, true},
		{"wrong", "front_desk", []string{"owner"}, false},
		{"empty list", "owner", nil, false},
		{"empty role", "", []string{"owner", "front_desk"}, false},
		{"undeclared", "old", []string{"owner", "front_desk"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := (User{Role: tt.role}).HasRole(tt.names...); got != tt.want {
				t.Errorf("HasRole = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUserRoleJSON(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(User{ID: 1, Email: "a@example.com", Role: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"role":"owner"`) {
		t.Fatalf("JSON = %s", data)
	}
}

func TestNewRoles(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		roles   *Roles
		invalid bool
	}{
		{"defaults", nil, false},
		{"custom", &Roles{[]string{"owner", "front_desk"}, "owner", "front_desk"}, false},
		{"empty", &Roles{}, true},
		{"unknown admin", &Roles{[]string{"owner"}, "missing", "owner"}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, err := pgxpool.New(t.Context(), "postgres://localhost/unused")
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			opts := []Option{withEnv(nil), func(o *options) { o.params = testParams; o.cleanup = 0 }}
			want := DefaultRoles()
			if tt.roles != nil {
				want = *tt.roles
				opts = append(opts, WithRoles(*tt.roles))
			}
			a, err := New(pool, opts...)
			if tt.invalid {
				if err == nil {
					_ = a.Close(t.Context())
					t.Fatal("New accepted invalid roles")
				}
				if !strings.Contains(err.Error(), "Try:") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := a.Close(t.Context()); err != nil {
					t.Error(err)
				}
			}()
			if got := a.Roles(); !reflect.DeepEqual(got, want) {
				t.Fatalf("Roles = %#v, want %#v", got, want)
			}
			copy := a.Roles()
			copy.Names[0] = "changed"
			if !reflect.DeepEqual(a.Roles(), want) {
				t.Fatal("Roles exposes the internal names")
			}
			if tt.roles != nil {
				tt.roles.Names[0] = "changed"
				if a.Roles().Names[0] == "changed" {
					t.Fatal("WithRoles retained the caller's slice")
				}
			}
		})
	}
}

func TestDefaultRolesCopy(t *testing.T) {
	t.Parallel()
	r := DefaultRoles()
	r.Names[0] = "changed"
	if DefaultRoles().Names[0] != "manager" {
		t.Fatal("DefaultRoles shares names")
	}
}
