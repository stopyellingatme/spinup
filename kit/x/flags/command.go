package flags

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/stopyellingatme/spinup/kit/audit"
	"github.com/stopyellingatme/spinup/kit/auth"
)

const commandUsage = "flags list [--json] | flags enable KEY | flags disable KEY | flags set KEY [--roles a,b] [--percent N] | flags user KEY --email E (--on|--off|--clear) | flags prune --yes"

// Command returns the operator command for flag rules. It never prompts.
// Register it with app.WithCommand("flags", flagStore.Command()).
func (s *Store) Command() func(ctx context.Context, args []string) error {
	return func(ctx context.Context, args []string) error {
		return s.runCommand(ctx, args, os.Stdout)
	}
}

func commandError(action, why string) error {
	return fmt.Errorf("%s: %s.\nTry: %s", action, why, commandUsage)
}

func (s *Store) runCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return commandError("flags", "no action was given")
	}
	action := "flags " + args[0]
	name := args[0]
	args = args[1:]
	ctx = audit.WithActor(ctx, audit.Operator())
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.Usage = func() {}
	var key string
	switch name {
	case "enable", "disable", "set", "user":
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return commandError(action, "a key is required")
		}
		key, args = args[0], args[1:]
	case "list", "prune":
	default:
		return commandError("flags", fmt.Sprintf("unknown action %q", name))
	}
	var asJSON, yes, on, off, clear bool
	var roles, email string
	var percent int
	switch name {
	case "list":
		f.BoolVar(&asJSON, "json", false, "print JSON")
	case "prune":
		f.BoolVar(&yes, "yes", false, "confirm deletion of stale rows")
	case "set":
		f.StringVar(&roles, "roles", "", "allowed roles, separated by commas")
		f.IntVar(&percent, "percent", 0, "percentage of signed-in users")
	case "user":
		f.StringVar(&email, "email", "", "account email")
		f.BoolVar(&on, "on", false, "enable for this account")
		f.BoolVar(&off, "off", false, "disable for this account")
		f.BoolVar(&clear, "clear", false, "remove the account rule")
	}
	if err := f.Parse(args); err != nil {
		return commandError(action, err.Error())
	}
	if f.NArg() != 0 {
		return commandError(action, fmt.Sprintf("unexpected argument %q", f.Arg(0)))
	}
	var text string
	var err error
	switch name {
	case "list":
		entries, err := s.List(ctx, time.Now().UTC())
		if err != nil {
			return fmt.Errorf("%s: %w", action, err)
		}
		return writeFlagList(out, entries, s.roles, asJSON)
	case "enable", "disable":
		if name == "enable" {
			err = s.Enable(ctx, key)
		} else {
			err = s.Disable(ctx, key)
		}
		text = "updated " + key + "\t" + onOff(name == "enable") + "\n"
	case "set":
		return s.setCommand(ctx, key, f, roles, percent, out)
	case "user":
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" {
			return commandError(action, "--email is required")
		}
		count := 0
		for _, v := range []bool{on, off, clear} {
			if v {
				count++
			}
		}
		if count != 1 {
			return commandError(action, "exactly one of --on, --off, or --clear is required")
		}
		state := onOff(on)
		if clear {
			err = s.ClearUser(ctx, key, email)
			state = "clear"
		} else {
			err = s.SetUser(ctx, key, email, on)
		}
		text = "updated " + key + "\t" + email + " " + state + "\n"
	case "prune":
		if !yes {
			return fmt.Errorf("flags prune: --yes is required, because the command deletes rows.\nTry: flags prune --yes")
		}
		var deleted []string
		deleted, err = s.Prune(ctx)
		if len(deleted) == 0 {
			text = "nothing to prune\n"
		}
		for _, key := range deleted {
			text += "deleted " + key + "\n"
		}
	}
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	return writeCommand(out, action, text)
}

func (s *Store) setCommand(ctx context.Context, key string, f *flag.FlagSet, roles string, percent int, out io.Writer) error {
	const action = "flags set"
	var hasRoles, hasPercent bool
	f.Visit(func(f *flag.Flag) {
		if f.Name == "roles" {
			hasRoles = true
		}
		if f.Name == "percent" {
			hasPercent = true
		}
	})
	if !hasRoles && !hasPercent {
		return commandError(action, "at least one of --roles or --percent is required")
	}
	// Validate both changes before the first write.
	if err := s.checkKey(key); err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	names := []string{}
	if roles != "" {
		names = strings.Split(roles, ",")
	}
	if hasRoles {
		var err error
		names, err = s.checkRoles(names)
		if err != nil {
			return fmt.Errorf("%s: %w", action, err)
		}
	}
	if hasPercent {
		if err := checkPercentage(percent); err != nil {
			return fmt.Errorf("%s: %w", action, err)
		}
	}
	if hasRoles {
		if err := s.SetRoles(ctx, key, names); err != nil {
			return fmt.Errorf("%s: %w", action, err)
		}
	}
	if hasPercent {
		if err := s.SetPercentage(ctx, key, percent); err != nil {
			return fmt.Errorf("%s: %w", action, err)
		}
	}
	entries, err := s.List(ctx, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	for _, e := range entries {
		if e.Key == key {
			return writeCommand(out, action, fmt.Sprintf("updated %s\troles=%s percent=%d\n", key, strings.Join(e.Roles, ","), e.Percentage))
		}
	}
	return fmt.Errorf("%s: %w", action, ErrUnknownKey)
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func writeCommand(out io.Writer, action, text string) error {
	if _, err := io.WriteString(out, text); err != nil {
		return fmt.Errorf("%s: cannot write output: %w\nTry: check standard output and retry", action, err)
	}
	return nil
}

func writeFlagList(out io.Writer, entries []Entry, roles auth.Roles, asJSON bool) error {
	if asJSON {
		err := json.NewEncoder(out).Encode(struct {
			Flags []Entry  `json:"flags"`
			Roles []string `json:"roles"`
		}{entries, roles.Names})
		if err != nil {
			return fmt.Errorf("flags list: cannot write JSON: %w\nTry: check standard output and retry", err)
		}
		return nil
	}
	var text strings.Builder
	w := tabwriter.NewWriter(&text, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "KEY\tSTATE\tROLES\tPERCENT\tUSERS\tNOTES")
	for _, e := range entries {
		names := make([]string, 0, len(e.Roles))
		for _, role := range e.Roles {
			if !roles.Valid(role) {
				role += "(undeclared)"
			}
			names = append(names, role)
		}
		roleText := strings.Join(names, ",")
		if roleText == "" {
			roleText = "-"
		}
		notes := []string{}
		if e.Public {
			notes = append(notes, "public")
		}
		if e.Default {
			notes = append(notes, "default on")
		}
		if e.Expires != "" {
			label := "expires "
			if e.Expired {
				label = "expired "
			}
			notes = append(notes, label+e.Expires)
		}
		if e.Stale {
			notes = append(notes, "stale")
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\n", e.Key, onOff(e.Enabled), roleText, e.Percentage, len(e.Users), strings.Join(notes, ","))
	}
	_ = w.Flush()
	return writeCommand(out, "flags list", text.String())
}
