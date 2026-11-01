package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/maintain/pg"
	"github.com/stopyellingatme/spinup/internal/maintain/pgupgrade"
)

func init() { registerMaintenance(newMaintenancePGInPlace) }

func newMaintenancePGInPlace(app *App) *cobra.Command {
	var o pg.Options
	c := &cobra.Command{Use: "pg-upgrade-in-place", Example: `  # Print the steps first
  spinup maintenance pg-upgrade-in-place --to 18 --plan --env production

  # Upgrade in place (this causes downtime)
  spinup maintenance pg-upgrade-in-place --to 18 --i-know --env production

  # Delete the old data directory after you verify the app
  spinup maintenance pg-upgrade-in-place --cleanup --i-know --env production`, Short: "Upgrade PostgreSQL with pg_upgrade --link and retain the old directory", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		opts := o
		opts.Dir, opts.Env = app.Flags.Dir, app.Flags.Env
		opts.Plan = opts.Plan || app.Flags.DryRun
		opts.Out, opts.ErrOut = c.OutOrStdout(), c.ErrOrStderr()
		opts.Confirm = confirmFor(app, c)
		return pgupgrade.Run(c.Context(), opts, pg.Deps{})
	}}
	c.Flags().IntVar(&o.To, "to", 0, "target PostgreSQL major: 17 or 18")
	c.Flags().BoolVar(&o.Plan, "plan", false, "print the steps without changes")
	c.Flags().BoolVar(&o.Now, "now", false, "override the maintenance window")
	c.Flags().BoolVar(&o.Cleanup, "cleanup", false, "remove the old data directory after verification")
	c.Flags().BoolVar(&o.IKnow, "i-know", false, "accept downtime and permanent changes to the old cluster")
	return c
}
