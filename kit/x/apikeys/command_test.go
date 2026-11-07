package apikeys

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestCommandArgumentErrors(t *testing.T) {
	s := &Store{}
	for _, args := range [][]string{
		nil, {"unknown"}, {"create"}, {"create", "x"}, {"create", "x", "--name"},
		{"create", "x", "--name", "x", "--scope", "bad"}, {"create", "x", "--name", "x", "--expires-days", "366"},
		{"create", "x", "--name", "x", "extra"}, {"create", "x", "--scope", "read"},
		{"revoke"}, {"revoke", "bad"}, {"revoke", "0"}, {"revoke", "-3"},
		{"plan"}, {"plan", "x"}, {"usage"}, {"usage", "x", "--days", "0"}, {"usage", "x", "--bad"},
		{"prune"}, {"prune", "--days", "3"}, {"prune", "--yes"}, {"prune", "--days", "0", "--yes"}, {"prune", "--days", "3", "--yes", "extra"},
		{"list", "extra"}, {"list", "--bad"},
	} {
		var out bytes.Buffer
		err := s.runCommand(t.Context(), args, &out)
		if err == nil || !strings.Contains(err.Error(), "\nTry: ") || !strings.Contains(err.Error(), "apikeys") || out.Len() != 0 {
			t.Errorf("%v: output=%q error=%v", args, out.String(), err)
		}
	}
	if err := s.Command()(t.Context(), []string{"nope"}); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("Command: %v", err)
	}
}

func TestCommandDatabase(t *testing.T) {
	pool := keyDB(t, true)
	cfg := testConfig()
	cfg.Plans["pro"] = Plan{PerMinute: 100, PerDay: 5000}
	s := dbStore(t, pool, cfg)
	id := keyUser(t, pool, "command@example.com")
	keyUser(t, pool, "other@example.com")
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := s.runCommand(t.Context(), args, &out)
		return out.String(), err
	}

	// create prints the token once, and the token authenticates.
	out, err := run("create", "Command@Example.com", "--name", "cli", "--scope", "write", "--expires-days", "30")
	if err != nil {
		t.Fatal(err)
	}
	token, rest, _ := strings.Cut(out, "\n")
	if !strings.HasPrefix(token, "key_") || !strings.Contains(rest, "cannot be shown again") {
		t.Fatalf("create output: %q", out)
	}
	if _, user, err := s.AuthenticateBearer(t.Context(), token); err != nil || user.ID != id {
		t.Fatalf("the created token does not authenticate: %v %v", user, err)
	}
	if _, err = run("create", "missing@example.com", "--name", "cli"); !errors.Is(err, ErrUserNotFound) || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("create for an unknown user: %v", err)
	}

	// list and list --json
	out, err = run("list")
	if err != nil || !strings.Contains(out, "command@example.com\tfree\t0") || strings.Contains(out, "other@example.com") {
		t.Fatalf("list: %q %v", out, err)
	}
	out, err = run("list", "--email", "COMMAND@example.com", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var accounts []Account
	if err = json.Unmarshal([]byte(out), &accounts); err != nil || len(accounts) != 1 || accounts[0].Email != "command@example.com" || len(accounts[0].Keys) != 1 || accounts[0].Keys[0].Name != "cli" {
		t.Fatalf("list --json: %q %v", out, err)
	}
	if out, err = run("list", "--email", "nobody@example.com", "--json"); err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("list for an unknown email: %q %v", out, err)
	}

	// plan
	if out, err = run("plan", "command@example.com", "pro"); err != nil || !strings.Contains(out, "updated") {
		t.Fatalf("plan: %q %v", out, err)
	}
	if name, _, err := s.PlanOf(t.Context(), id); err != nil || name != "pro" {
		t.Fatalf("plan after the command: %q %v", name, err)
	}
	if _, err = run("plan", "command@example.com", "gold"); !errors.Is(err, ErrUnknownPlan) || !strings.Contains(err.Error(), "Try:") || !strings.Contains(err.Error(), "free, pro") {
		t.Fatalf("plan with an unknown plan: %v", err)
	}
	if _, err = run("plan", "missing@example.com", "pro"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("plan for an unknown user: %v", err)
	}

	// usage --json
	if _, err = pool.Exec(t.Context(), `INSERT INTO api_key_usage(key_id, day, count) SELECT id, current_date, 7 FROM api_keys WHERE user_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	out, err = run("usage", "command@example.com", "--days", "30", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var usage []UsageDay
	if err = json.Unmarshal([]byte(out), &usage); err != nil || len(usage) != 1 || usage[0].Count != 7 {
		t.Fatalf("usage --json: %q %v", out, err)
	}
	if out, err = run("usage", "command@example.com"); err != nil || !strings.Contains(out, "\t7\n") {
		t.Fatalf("usage: %q %v", out, err)
	}
	if _, err = run("usage", "missing@example.com"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("usage for an unknown user: %v", err)
	}

	// revoke
	keyID := strconv.FormatInt(accounts[0].Keys[0].ID, 10)
	if _, err = run("revoke", "999999"); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("revoke of an unknown key: %v", err)
	}
	if out, err = run("revoke", keyID); err != nil || !strings.Contains(out, "revoked "+keyID) {
		t.Fatalf("revoke: %q %v", out, err)
	}
	if _, _, err = s.AuthenticateBearer(t.Context(), token); err == nil {
		t.Fatal("a revoked key still authenticates")
	}
	if _, err = run("revoke", keyID); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("second revoke: %v", err)
	}

	// prune refuses without --yes, and deletes with it.
	if _, err = run("prune", "--days", "1"); err == nil || !strings.Contains(err.Error(), "--yes") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("prune without --yes: %v", err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE api_key_usage SET day = current_date - 400`); err != nil {
		t.Fatal(err)
	}
	if out, err = run("prune", "--days", "30", "--yes"); err != nil || !strings.HasPrefix(out, "pruned ") || strings.HasPrefix(out, "pruned 0 ") {
		t.Fatalf("prune: %q %v", out, err)
	}
	var left int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM api_key_usage`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("usage rows left: %d %v", left, err)
	}

	// The commands that change rows record the operator.
	var operators int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE actor_kind='operator' AND table_name IN ('api_keys','api_key_plans','api_key_usage')`).Scan(&operators); err != nil || operators < 3 {
		t.Fatalf("operator audit events: %d %v", operators, err)
	}
	if err = s.Command()(t.Context(), []string{"list"}); err != nil {
		t.Fatal(err)
	}
}
