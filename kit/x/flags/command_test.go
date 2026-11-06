package flags

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/auth"
)

func TestCommandValidation(t *testing.T) {
	t.Parallel()
	s, _, _ := storeFixture(t, lazyStorePool(t))
	for _, tc := range []struct{ args, want string }{
		{"", "no action"}, {"bogus", "unknown action"}, {"list extra", "unexpected argument"},
		{"list --bad", "flag provided but not defined"}, {"enable", "key is required"},
		{"disable a b", "unexpected argument"}, {"enable --help", "key is required"},
		{"set new_checkout", "at least one"}, {"set new_checkout --percent nope", "invalid value"},
		{"user new_checkout --email a@b.c", "exactly one"}, {"user new_checkout --on --off --email a@b.c", "exactly one"},
		{"user new_checkout --on", "--email is required"}, {"prune", "--yes is required, because the command deletes rows"},
		{"prune --yes extra", "unexpected argument"},
	} {
		t.Run(tc.args, func(t *testing.T) {
			var out bytes.Buffer
			err := s.runCommand(context.Background(), strings.Fields(tc.args), &out)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "\nTry: ") || out.Len() != 0 {
				t.Fatalf("output=%q error=%v", out.String(), err)
			}
		})
	}
	for _, tc := range []struct {
		args   []string
		target error
	}{
		{[]string{"enable", "missing"}, ErrUnknownKey},
		{[]string{"set", "new_checkout", "--roles", "missing"}, ErrUnknownRole},
		{[]string{"set", "new_checkout", "--roles", "manager", "--percent", "101"}, ErrPercentage},
	} {
		err := s.runCommand(t.Context(), tc.args, io.Discard)
		if !errors.Is(err, tc.target) || !strings.HasPrefix(err.Error(), "flags ") {
			t.Fatalf("error = %v", err)
		}
	}
	if err := s.Command()(t.Context(), nil); err == nil {
		t.Fatal("Command accepted no action")
	}
}

func TestCommandListOutput(t *testing.T) {
	t.Parallel()
	roles := auth.Roles{Names: []string{"manager", "staff"}, Default: "staff", Admin: "manager"}
	entries := []Entry{
		{Key: "new_checkout", Enabled: true, Public: true, Default: true, Roles: []string{"manager"}, Percentage: 50, Users: []UserRule{{Email: "a@b.c"}}, Expires: "2027-03-31"},
		{Key: "beta", Roles: []string{"cook"}, Stale: true, Expired: true, Expires: "2026-01-01"},
		{Key: "empty"},
	}
	var out bytes.Buffer
	if err := writeFlagList(&out, entries, roles, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"KEY", "STATE", "PERCENT", "USERS", "new_checkout", "cook(undeclared)", "public,default on,expires 2027-03-31", "expired 2026-01-01,stale"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
	out.Reset()
	if err := writeFlagList(&out, entries, roles, true); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Flags []Entry  `json:"flags"`
		Roles []string `json:"roles"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Flags) != 3 || len(got.Roles) != 2 {
		t.Fatalf("json = %s", out.String())
	}
	if err := writeFlagList(commandBadWriter{}, entries, roles, false); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("error = %v", err)
	}
	if err := writeFlagList(commandBadWriter{}, entries, roles, true); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("error = %v", err)
	}
}

type commandBadWriter struct{}

func (commandBadWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCommandDatabase(t *testing.T) {
	s, _, _ := storeFixture(t, flagDB(t, true))
	t.Setenv("USER", "flag-operator")
	t.Setenv("SUDO_USER", "")
	if _, err := s.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	storeExec(t, s.pool, `INSERT INTO users (email, password_hash, role) VALUES ('ana@example.com', 'hash', 'staff'); INSERT INTO feature_flags (key, enabled) VALUES ('removed', false)`)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"enable", "new_checkout"}, "updated new_checkout\ton\n"},
		{[]string{"disable", "new_checkout"}, "updated new_checkout\toff\n"},
		{[]string{"set", "new_checkout", "--roles", "manager", "--percent", "50"}, "updated new_checkout\troles=manager percent=50\n"},
		{[]string{"set", "new_checkout", "--roles", ""}, "updated new_checkout\troles= percent=50\n"},
		{[]string{"set", "new_checkout", "--percent", "0"}, "updated new_checkout\troles= percent=0\n"},
		{[]string{"user", "new_checkout", "--email", "ANA@example.com", "--on"}, "updated new_checkout\tana@example.com on\n"},
		{[]string{"user", "new_checkout", "--email", "ana@example.com", "--off"}, "updated new_checkout\tana@example.com off\n"},
		{[]string{"user", "new_checkout", "--email", "ana@example.com", "--clear"}, "updated new_checkout\tana@example.com clear\n"},
		{[]string{"prune", "--yes"}, "deleted removed\n"},
		{[]string{"prune", "--yes"}, "nothing to prune\n"},
	} {
		var out bytes.Buffer
		if err := s.runCommand(t.Context(), tc.args, &out); err != nil {
			t.Fatal(err)
		}
		if out.String() != tc.want {
			t.Fatalf("%v: got %q want %q", tc.args, out.String(), tc.want)
		}
	}
	var out bytes.Buffer
	if err := s.runCommand(t.Context(), []string{"list", "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"key":"new_checkout"`) {
		t.Fatal(out.String())
	}
	if err := s.runCommand(t.Context(), []string{"user", "new_checkout", "--email", "missing@example.com", "--on"}, io.Discard); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("error = %v", err)
	}
	var actor, source string
	if err := s.pool.QueryRow(t.Context(), `SELECT actor_email, source FROM audit_events WHERE table_name = 'feature_flags' AND action = 'update' ORDER BY id DESC LIMIT 1`).Scan(&actor, &source); err != nil {
		t.Fatal(err)
	}
	if actor != "operator:flag-operator" || source != "cli" {
		t.Fatalf("actor=%q source=%q", actor, source)
	}
	entries, err := s.List(t.Context(), time.Now())
	if err != nil || len(entries) == 0 {
		t.Fatalf("entries=%v error=%v", entries, err)
	}
}
