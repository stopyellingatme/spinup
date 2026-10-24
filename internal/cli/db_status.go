package cli

import (
	"os"

	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/db/ops"
	exec "github.com/stopyellingatme/spinup/internal/exec"
)

// dbSubcommands holds the builders of the db subcommands that register with registerDB.
var dbSubcommands []func(app *App) *cobra.Command

// registerDB adds a subcommand to `spinup db`. Call it from init().
func registerDB(build func(app *App) *cobra.Command) {
	regMu.Lock()
	defer regMu.Unlock()
	dbSubcommands = append(dbSubcommands, build)
}

// dbOptions maps the global flags to the options of a database command.
func dbOptions(app *App, c *cobra.Command, know bool) ops.Options {
	return ops.Options{
		Dir: app.Flags.Dir, Env: app.Flags.Env, IKnow: know,
		Interactive: app.UI.Interactive(), Yes: app.Flags.Yes, CI: os.Getenv("CI") != "", ConfirmName: app.Flags.ConfirmEnv, DryRun: app.Flags.DryRun,
		In: c.InOrStdin(), Out: c.OutOrStdout(), ErrOut: c.ErrOrStderr(),
	}
}

func init() { registerDB(newDBStatus) }

func newDBStatus(app *App) *cobra.Command {
	return &cobra.Command{Use: "status", Short: "List applied and pending migrations", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return ops.Status(c.Context(), dbOptions(app, c, false), exec.OSRunner{})
	}}
}
