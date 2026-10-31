package auth

import (
	"bytes"
	"strings"
	"testing"
)

func TestUserCommandUsage(t *testing.T) {
	t.Parallel()
	a := &Auth{roles: Roles{Names: []string{"owner", "cook"}, Admin: "owner", Default: "cook"}}
	for _, tt := range []struct {
		name string
		args []string
		want []string
	}{
		{"no action", nil, []string{"no action", "Try:"}},
		{"unknown action", []string{"remove"}, []string{`unknown action "remove"`, "create, set-role, list, backfill", "Try:"}},
		{"create email", []string{"create", "--role", "cook", "--password-stdin"}, []string{"--email is required", "Try:"}},
		{"create role", []string{"create", "--email", "a@example.com", "--password-stdin"}, []string{"--role is required", "Declared roles: owner, cook.", "Try:"}},
		{"create password input", []string{"create", "--email", "a@example.com", "--role", "cook"}, []string{"--password-stdin is required", "Try:"}},
		{"create unknown role", []string{"create", "--email", "a@example.com", "--role", "pilot", "--password-stdin"}, []string{`role "pilot" is not declared`, "Declared roles: owner, cook.", "Try:"}},
		{"set role email", []string{"set-role", "--role", "cook"}, []string{"--email is required", "Try:"}},
		{"set role role", []string{"set-role", "--email", "a@example.com"}, []string{"--role is required", "Declared roles: owner, cook.", "Try:"}},
		{"list arguments", []string{"list", "extra"}, []string{"unexpected arguments", "Try:"}},
		{"backfill arguments", []string{"backfill", "extra"}, []string{"unexpected arguments", "Try:"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			err := runUserCommand(t.Context(), a, tt.args, strings.NewReader("correct horse battery\n"), &out)
			if err == nil {
				t.Fatal("runUserCommand returned nil")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, does not contain %q", err, want)
				}
			}
		})
	}
}

func TestUserCommandStore(t *testing.T) {
	t.Parallel()
	roles := Roles{Names: []string{"owner", "cook"}, Admin: "owner", Default: "cook"}
	f := newFixture(t, WithRoles(roles))
	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := runUserCommand(t.Context(), f.auth, args, strings.NewReader("correct horse battery\n"), &out); err != nil {
			t.Fatalf("user %s: %v", strings.Join(args, " "), err)
		}
		return out.String()
	}

	run("create", "--email", "OWNER@Example.com", "--role", "owner", "--password-stdin")
	run("create", "--email", "cook@example.com", "--role", "cook", "--password-stdin")
	if err := f.auth.SetRole(t.Context(), "cook@example.com", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO users(email, password_hash, role) VALUES ('empty@example.com', 'unused', ''), ('retired@example.com', 'unused', 'retired')`); err != nil {
		t.Fatal(err)
	}

	if got, want := run("list"), "cook@example.com\towner\nempty@example.com\tcook (default)\nowner@example.com\towner\nretired@example.com\tretired (undeclared)\n"; got != want {
		t.Errorf("user list output = %q, want %q", got, want)
	}
	if got, want := run("backfill"), "backfilled 1 accounts\n"; got != want {
		t.Errorf("user backfill output = %q, want %q", got, want)
	}
	if got, want := run("list"), "cook@example.com\towner\nempty@example.com\tcook\nowner@example.com\towner\nretired@example.com\tretired (undeclared)\n"; got != want {
		t.Errorf("user list after backfill = %q, want %q", got, want)
	}
}

func TestUserCommandListNoneRole(t *testing.T) {
	t.Parallel()
	roles := Roles{Names: []string{"owner", "cook"}, Admin: "owner", Default: "owner"}
	f := newFixture(t, WithRoles(roles))
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO users(email, password_hash, role) VALUES ('empty@example.com', 'unused', '')`); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runUserCommand(t.Context(), f.auth, []string{"list"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "empty@example.com\t(none)\n"; got != want {
		t.Errorf("user list output = %q, want %q", got, want)
	}
}
