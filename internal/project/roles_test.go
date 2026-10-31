package project

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestAuthRolesConfig(t *testing.T) {
	t.Parallel()
	defaults := AuthRoles{Names: []string{"manager", "staff"}, Admin: "manager", Default: "staff"}
	tests := []struct {
		name, table string
		want        AuthRoles
		invalid     bool
	}{
		{"absent", "", defaults, false},
		{"empty table", "[auth]\n", defaults, false},
		{"names only", "[auth]\nroles = ['manager', 'staff', 'front_desk']\n", AuthRoles{Names: []string{"manager", "staff", "front_desk"}, Admin: "manager", Default: "staff"}, false},
		{"admin only", "[auth]\nadmin_role = 'staff'\n", AuthRoles{Names: defaults.Names, Admin: "staff", Default: "staff"}, false},
		{"default only", "[auth]\ndefault_role = 'manager'\n", AuthRoles{Names: defaults.Names, Admin: "manager", Default: "manager"}, false},
		{"custom", "[auth]\nroles = ['owner', 'front_desk']\nadmin_role = 'owner'\ndefault_role = 'front_desk'\n", AuthRoles{Names: []string{"owner", "front_desk"}, Admin: "owner", Default: "front_desk"}, false},
		{"explicit empty admin", "[auth]\nadmin_role = ''\n", AuthRoles{Names: defaults.Names, Admin: "", Default: "staff"}, true},
		{"explicit empty default", "[auth]\ndefault_role = ''\n", AuthRoles{Names: defaults.Names, Admin: "manager", Default: ""}, true},
		{"explicit empty names", "[auth]\nroles = []\n", AuthRoles{Names: []string{}, Admin: "manager", Default: "staff"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, err := Parse([]byte("[app]\nname = 'demo'\nmodule = 'example.com/demo'\n" + tt.table))
			if err != nil {
				t.Fatal(err)
			}
			if got := c.AuthRoles(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("AuthRoles = %#v, want %#v", got, tt.want)
			}
			if _, ok := c.Extra["auth"]; ok {
				t.Fatal("auth remained in Extra")
			}
			if err := c.Validate(); (err != nil) != tt.invalid {
				t.Fatalf("Validate = %v, invalid = %v", err, tt.invalid)
			}
		})
	}
}

func TestAuthRolesValidate(t *testing.T) {
	t.Parallel()
	many := make([]string, 17)
	for i := range many {
		many[i] = fmt.Sprintf("role%d", i)
	}
	tests := []struct {
		name        string
		roles       AuthRoles
		rule, value string
	}{
		{"defaults", (&Config{}).AuthRoles(), "", ""},
		{"single", AuthRoles{[]string{"owner"}, "owner", "owner"}, "", ""},
		{"sixteen", AuthRoles{many[:16], "role0", "role15"}, "", ""},
		{"length boundary", AuthRoles{[]string{strings.Repeat("a", 32)}, strings.Repeat("a", 32), strings.Repeat("a", 32)}, "", ""},
		{"empty list", AuthRoles{}, "1 to 16", "0"},
		{"large list", AuthRoles{many, "role0", "role1"}, "1 to 16", "17"},
		{"duplicate", AuthRoles{[]string{"owner", "owner"}, "owner", "owner"}, "unique", "owner"},
		{"empty name", AuthRoles{[]string{""}, "", ""}, "must match", `""`},
		{"uppercase", AuthRoles{[]string{"Owner"}, "Owner", "Owner"}, "must match", "Owner"},
		{"leading digit", AuthRoles{[]string{"1a"}, "1a", "1a"}, "must match", "1a"},
		{"hyphen", AuthRoles{[]string{"front-desk"}, "front-desk", "front-desk"}, "must match", "front-desk"},
		{"unicode", AuthRoles{[]string{"café"}, "café", "café"}, "must match", "café"},
		{"too long", AuthRoles{[]string{strings.Repeat("a", 33)}, "a", "a"}, "must match", strings.Repeat("a", 33)},
		{"double underscore", AuthRoles{[]string{"a_b", "a__b"}, "a_b", "a_b"}, "double underscore", "a__b"},
		{"trailing underscore", AuthRoles{[]string{"a_", "a"}, "a", "a"}, "trailing underscore", "a_"},
		{"constant collision", AuthRoles{[]string{"a1", "a_1"}, "a1", "a1"}, "RoleConst", "A1"},
		{"reserved constant", AuthRoles{[]string{"set"}, "set", "set"}, "Set", "set"},
		{"missing admin", AuthRoles{[]string{"owner"}, "missing", "owner"}, "admin_role", "missing"},
		{"missing default", AuthRoles{[]string{"owner"}, "owner", "missing"}, "default_role", "missing"},
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
			c, err := Parse([]byte("[app]\nname = 'demo'\nmodule = 'example.com/demo'\n"))
			if err != nil {
				t.Fatal(err)
			}
			c.Auth.Roles, c.Auth.AdminRole, c.Auth.DefaultRole = tt.roles.Names, tt.roles.Admin, tt.roles.Default
			if tt.roles.Names != nil && c.Validate() == nil {
				t.Fatal("Config.Validate accepted invalid roles")
			}
		})
	}
}

func TestRoleConst(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, want string }{{"manager", "Manager"}, {"front_desk", "FrontDesk"}, {"a_1", "A1"}, {"role2", "Role2"}} {
		if got := RoleConst(tt.name); got != tt.want {
			t.Errorf("RoleConst(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestRenderRolesFile(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		roles AuthRoles
	}{
		{"defaults", (&Config{}).AuthRoles()},
		{"custom", AuthRoles{[]string{"owner", "front_desk"}, "owner", "front_desk"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := RenderRolesFile(tt.roles)
			want, err := os.ReadFile("testdata/roles_" + tt.name + ".golden")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("render =\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestRolesPackMatchesDefaults(t *testing.T) {
	t.Parallel()
	got, err := os.ReadFile("../templates/packs/auth/files/" + RolesFilePath)
	if err != nil {
		t.Fatal(err)
	}
	if want := RenderRolesFile((&Config{}).AuthRoles()); !bytes.Equal(got, want) {
		t.Fatalf("pack differs from RenderRolesFile:\n%s", got)
	}
}

func TestConfigRolesErrorEndsWithTry(t *testing.T) {
	t.Parallel()
	c := &Config{}
	c.Auth.Roles = []string{}
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate accepted an empty role list")
	}
	lines := strings.Split(err.Error(), "\n")
	if !strings.HasPrefix(lines[len(lines)-1], "Try:") {
		t.Fatalf("error does not end with a Try line: %v", err)
	}
}
