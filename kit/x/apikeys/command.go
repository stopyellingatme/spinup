package apikeys

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stopyellingatme/spinup/kit/audit"
)

const commandUsage = "apikeys list [--email <email>] [--json] | apikeys create <email> --name <name> [--scope read|write] [--expires-days n] | apikeys revoke <id> | apikeys plan <email> <plan> | apikeys usage <email> [--days 30] [--json] | apikeys prune --days <n> --yes"

// Command returns the noninteractive operator command.
func (s *Store) Command() func(context.Context, []string) error {
	return func(ctx context.Context, args []string) error { return s.runCommand(ctx, args, os.Stdout) }
}
func commandError(action, why string) error {
	return fmt.Errorf("%s: %s.\nTry: %s", action, why, commandUsage)
}
func (s *Store) commandUser(ctx context.Context, email string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, "SELECT id FROM users WHERE email=$1", strings.ToLower(strings.TrimSpace(email))).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrUserNotFound
	}
	if err != nil {
		return 0, storeFailure("find the user", err)
	}
	return id, nil
}
func (s *Store) runCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return commandError("apikeys", "no action was given")
	}
	name := args[0]
	args = args[1:]
	action := "apikeys " + name
	switch name {
	case "list", "create", "revoke", "plan", "usage", "prune":
	default:
		return commandError("apikeys", fmt.Sprintf("unknown action %q", name))
	}
	ctx = audit.WithActor(ctx, audit.Operator())
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var positional []string
	n := 0
	switch name {
	case "create", "revoke", "usage":
		n = 1
	case "plan":
		n = 2
	}
	for i := 0; i < n; i++ {
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return commandError(action, "missing required argument")
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
	var email, keyName, scope string
	var asJSON, yes bool
	var days, expires int
	switch name {
	case "list":
		f.StringVar(&email, "email", "", "account email")
		f.BoolVar(&asJSON, "json", false, "JSON output")
	case "create":
		f.StringVar(&keyName, "name", "", "key name")
		f.StringVar(&scope, "scope", "read", "scope")
		f.IntVar(&expires, "expires-days", 0, "expiry")
	case "usage":
		f.IntVar(&days, "days", 30, "days")
		f.BoolVar(&asJSON, "json", false, "JSON output")
	case "prune":
		f.IntVar(&days, "days", 0, "days")
		f.BoolVar(&yes, "yes", false, "confirm")
	}
	if err := f.Parse(args); err != nil {
		return commandError(action, err.Error())
	}
	if f.NArg() != 0 {
		return commandError(action, "unexpected argument "+f.Arg(0))
	}
	if name == "create" {
		if keyName == "" {
			return commandError(action, "--name is required")
		}
		if expires < 0 || expires > 365 {
			return commandError(action, "--expires-days must be 0 to 365")
		}
		if err := validateCreate(CreateInput{Name: keyName, Scope: Scope(scope), ExpiresIn: time.Duration(expires) * 24 * time.Hour}); err != nil {
			return commandError(action, err.Error())
		}
	}
	if name == "prune" && !yes {
		return commandError(action, "--yes is required")
	}
	if (name == "prune" || name == "usage") && (days < 1 || days > 100000) {
		return commandError(action, "--days must be 1 to 100000")
	}
	var value any
	var text string
	var err error
	switch name {
	case "list":
		var accounts []Account
		accounts, err = s.Accounts(ctx)
		if email != "" {
			filtered := []Account{}
			for _, a := range accounts {
				if a.Email == strings.ToLower(strings.TrimSpace(email)) {
					filtered = append(filtered, a)
				}
			}
			accounts = filtered
		}
		value = accounts
		for _, a := range accounts {
			text += fmt.Sprintf("%s\t%s\t%d\n", a.Email, a.Plan, a.Today)
		}
	case "create":
		var id int64
		id, err = s.commandUser(ctx, positional[0])
		if err == nil {
			var token string
			_, token, err = s.Create(ctx, id, CreateInput{Name: keyName, Scope: Scope(scope), ExpiresIn: time.Duration(expires) * 24 * time.Hour})
			text = token + "\nThis token cannot be shown again.\n"
		}
	case "revoke":
		var id int64
		id, err = strconv.ParseInt(positional[0], 10, 64)
		if err != nil || id <= 0 {
			return commandError(action, "the ID must be a positive integer")
		}
		err = s.Revoke(ctx, 0, id)
		text = "revoked " + positional[0] + "\n"
	case "plan":
		err = s.SetPlan(ctx, positional[0], positional[1])
		text = "updated " + positional[0] + "\t" + positional[1] + "\n"
	case "usage":
		var id int64
		id, err = s.commandUser(ctx, positional[0])
		if err == nil {
			var usage []UsageDay
			usage, err = s.Usage(ctx, id, days)
			value = usage
			for _, u := range usage {
				text += fmt.Sprintf("%s\t%d\t%d\n", u.Day, u.KeyID, u.Count)
			}
		}
	case "prune":
		var count int64
		count, err = s.Prune(ctx, time.Duration(days)*24*time.Hour)
		text = fmt.Sprintf("pruned %d rows\n", count)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	if asJSON {
		if err = json.NewEncoder(out).Encode(value); err != nil {
			return commandError(action, "cannot write JSON: "+err.Error())
		}
		return nil
	}
	if _, err = io.WriteString(out, text); err != nil {
		return commandError(action, "cannot write output: "+err.Error())
	}
	return nil
}
