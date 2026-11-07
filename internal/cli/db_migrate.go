package cli

import (
	"os"

	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/db"
	exec "github.com/stopyellingatme/spinup/internal/exec"
)

func newDBMigrate(app *App) *cobra.Command {
	var know bool
	c := &cobra.Command{Use: "migrate [up|down|status] | migrate kit up|down|status|to <version>|version", Example: `  # Apply every pending migration
  spinup db migrate up

  # Roll back the last migration (asks you to confirm data loss)
  spinup db migrate down --i-know

  # List applied and pending migrations
  spinup db migrate status

  # List the kit migrations of the auth and audit modules (ADR 0028)
  spinup db migrate kit status`, Short: "Run migrations through the application API", Args: migrateArgs, RunE: func(c *cobra.Command, args []string) error {
		sub, kit, version := "up", false, ""
		if len(args) > 0 && args[0] == "kit" {
			kit, sub = true, ""
			if len(args) > 1 {
				sub = args[1]
			}
			if len(args) > 2 {
				version = args[2]
			}
		} else if len(args) > 0 {
			sub = args[0]
		}
		return db.Migrate(c.Context(), db.Options{Dir: app.Flags.Dir, Sub: sub, Kit: kit, Version: version, Env: app.Flags.Env, IKnow: know, Interactive: app.UI.Interactive(), Yes: app.Flags.Yes, CI: os.Getenv("CI") != "", ConfirmName: app.Flags.ConfirmEnv, DryRun: app.Flags.DryRun, In: c.InOrStdin(), Out: c.OutOrStdout(), ErrOut: c.ErrOrStderr()}, exec.OSRunner{})
	}}
	c.Flags().BoolVar(&know, "i-know", false, "acknowledge that down can lose data")
	return c
}

// migrateArgs allows one subcommand, or `kit` with a subcommand and an optional version.
func migrateArgs(c *cobra.Command, args []string) error {
	if len(args) > 0 && args[0] == "kit" {
		return cobra.MaximumNArgs(3)(c, args)
	}
	return cobra.MaximumNArgs(1)(c, args)
}
