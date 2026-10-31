package auth

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const userCommandUsage = "user create --email E --role R --password-stdin | user set-role --email E --role R | user list | user backfill"

// UserCommand returns the operator command for account and role management.
// Register it with app.WithCommand("user", auth.UserCommand(a)).
func UserCommand(a *Auth) func(ctx context.Context, args []string) error {
	return func(ctx context.Context, args []string) error {
		return runUserCommand(ctx, a, args, os.Stdin, os.Stdout)
	}
}

func runUserCommand(ctx context.Context, a *Auth, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return userCommandError("user", "no action was given", userCommandUsage)
	}
	switch args[0] {
	case "create":
		return createUserCommand(ctx, a, args[1:], in, out)
	case "set-role":
		return setUserRoleCommand(ctx, a, args[1:], out)
	case "list":
		return listUsersCommand(ctx, a, args[1:], out)
	case "backfill":
		return backfillRolesCommand(ctx, a, args[1:], out)
	default:
		return userCommandError("user", fmt.Sprintf("unknown action %q. Available actions: create, set-role, list, backfill", args[0]), userCommandUsage)
	}
}

func createUserCommand(ctx context.Context, a *Auth, args []string, in io.Reader, out io.Writer) error {
	flags := newUserFlagSet("create")
	email := flags.String("email", "", "account email")
	role := flags.String("role", "", "declared role")
	passwordStdin := flags.Bool("password-stdin", false, "read the password from standard input")
	if err := parseUserFlags(flags, args); err != nil {
		return userCommandError("user create", "invalid flags: "+err.Error(), "user create --email a@b.c --role staff --password-stdin")
	}
	if flags.NArg() != 0 {
		return userCommandError("user create", fmt.Sprintf("unexpected arguments %q", flags.Args()), "user create --email a@b.c --role staff --password-stdin")
	}
	if *email == "" {
		return userCommandError("user create", "--email is required", "user create --email a@b.c --role staff --password-stdin")
	}
	if *role == "" {
		return userCommandError("user create", "--role is required. Declared roles: "+declaredRoles(a), "user create --email a@b.c --role "+a.Roles().Default+" --password-stdin")
	}
	if err := a.checkRole(*role); err != nil {
		return fmt.Errorf("user create: %w", err)
	}
	if !*passwordStdin {
		return userCommandError("user create", "--password-stdin is required", "user create --email a@b.c --role "+*role+" --password-stdin")
	}
	password, err := readUserPassword(in)
	if err != nil {
		return fmt.Errorf("user create: cannot read the password from standard input: %w\nTry: provide the password through --password-stdin", err)
	}
	user, err := a.CreateUser(ctx, *email, password, *role)
	if err != nil {
		return fmt.Errorf("user create: %w", err)
	}
	return writeUserCommand(out, "user create", "created "+user.Email+"\t"+user.Role+"\n")
}

func setUserRoleCommand(ctx context.Context, a *Auth, args []string, out io.Writer) error {
	flags := newUserFlagSet("set-role")
	email := flags.String("email", "", "account email")
	role := flags.String("role", "", "declared role")
	if err := parseUserFlags(flags, args); err != nil {
		return userCommandError("user set-role", "invalid flags: "+err.Error(), "user set-role --email a@b.c --role staff")
	}
	if flags.NArg() != 0 {
		return userCommandError("user set-role", fmt.Sprintf("unexpected arguments %q", flags.Args()), "user set-role --email a@b.c --role staff")
	}
	if *email == "" {
		return userCommandError("user set-role", "--email is required", "user set-role --email a@b.c --role staff")
	}
	if *role == "" {
		return userCommandError("user set-role", "--role is required. Declared roles: "+declaredRoles(a), "user set-role --email a@b.c --role "+a.Roles().Default)
	}
	if err := a.SetRole(ctx, *email, *role); err != nil {
		return fmt.Errorf("user set-role: %w", err)
	}
	return writeUserCommand(out, "user set-role", "updated "+*email+"\t"+*role+"\n")
}

func listUsersCommand(ctx context.Context, a *Auth, args []string, out io.Writer) error {
	if len(args) != 0 {
		return userCommandError("user list", fmt.Sprintf("unexpected arguments %q", args), "user list")
	}
	users, err := a.ListUsers(ctx)
	if err != nil {
		return fmt.Errorf("user list: %w", err)
	}
	roles := a.Roles()
	for _, user := range users {
		if err := writeUserCommand(out, "user list", user.Email+"\t"+listedRole(user.Role, roles)+"\n"); err != nil {
			return err
		}
	}
	return nil
}

func backfillRolesCommand(ctx context.Context, a *Auth, args []string, out io.Writer) error {
	if len(args) != 0 {
		return userCommandError("user backfill", fmt.Sprintf("unexpected arguments %q", args), "user backfill")
	}
	count, err := a.BackfillRoles(ctx)
	if err != nil {
		return fmt.Errorf("user backfill: %w", err)
	}
	return writeUserCommand(out, "user backfill", fmt.Sprintf("backfilled %d accounts\n", count))
}

func newUserFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}
	return flags
}

func parseUserFlags(flags *flag.FlagSet, args []string) error {
	return flags.Parse(args)
}

func readUserPassword(in io.Reader) (string, error) {
	password, err := io.ReadAll(in)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r"), nil
}

func declaredRoles(a *Auth) string {
	return strings.Join(a.Roles().Names, ", ")
}

func listedRole(role string, roles Roles) string {
	switch {
	case role == "" && roles.Default == roles.Admin:
		return "(none)"
	case role == "":
		return roles.Default + " (default)"
	case !roles.Valid(role):
		return role + " (undeclared)"
	default:
		return role
	}
}

func writeUserCommand(out io.Writer, action, text string) error {
	if _, err := io.WriteString(out, text); err != nil {
		return fmt.Errorf("%s: cannot write output: %w\nTry: check standard output and retry", action, err)
	}
	return nil
}

func userCommandError(action, why, try string) error {
	return fmt.Errorf("%s: %s.\nTry: %s", action, why, try)
}
