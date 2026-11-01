package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/maintain/pg"
)

func init() {
	registerMaintenance(newMaintenancePGMinor)
	registerMaintenance(newMaintenancePGMajor)
}
func newMaintenancePGMinor(app *App) *cobra.Command { return newMaintenancePG(app, false) }
func newMaintenancePGMajor(app *App) *cobra.Command { return newMaintenancePG(app, true) }
func newMaintenancePG(app *App, major bool) *cobra.Command {
	o := pg.Options{Major: major}
	use, short := "pg-minor-upgrade", "Upgrade PostgreSQL to the pinned minor after a backup"
	example := "  # Print the steps first\n  spinup maintenance pg-minor-upgrade --plan --env production\n\n  # Upgrade now, even outside the maintenance window\n  spinup maintenance pg-minor-upgrade --now --env production"
	if major {
		use, short = "pg-major-upgrade", "Upgrade the PostgreSQL major with a verified dump and retained old volume"
		example = "  # Print the steps first\n  spinup maintenance pg-major-upgrade --to 18 --plan --env production\n\n  # Upgrade (this causes downtime)\n  spinup maintenance pg-major-upgrade --to 18 --i-know --env production\n\n  # Remove the old volume after you verify the app\n  spinup maintenance pg-major-upgrade --cleanup --i-know --env production"
	}
	c := &cobra.Command{Use: use, Short: short, Example: example, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		opts := o
		opts.Dir, opts.Env = app.Flags.Dir, app.Flags.Env
		opts.Plan = opts.Plan || app.Flags.DryRun
		opts.Out, opts.ErrOut = c.OutOrStdout(), c.ErrOrStderr()
		opts.Confirm = confirmFor(app, c)
		return pg.Run(c.Context(), opts, pg.Deps{})
	}}
	c.Flags().BoolVar(&o.Plan, "plan", false, "print the steps without changes")
	c.Flags().BoolVar(&o.Now, "now", false, "override the maintenance window")
	if major {
		c.Flags().IntVar(&o.To, "to", 0, "target PostgreSQL major: 16, 17, or 18")
		c.Flags().BoolVar(&o.Cleanup, "cleanup", false, "remove the retained old volume after a completed upgrade")
		c.Flags().BoolVar(&o.IKnow, "i-know", false, "accept app downtime or permanent removal of the old volume")
	}
	return c
}
