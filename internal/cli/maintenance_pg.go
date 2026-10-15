package cli

import (
	"os"

	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/maintain/pg"
	"github.com/stopyellingatme/spinup/internal/project"
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
	if major {
		use, short = "pg-major-upgrade", "Upgrade the PostgreSQL major with a verified dump and retained old volume"
	}
	c := &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		opts := o
		opts.Dir, opts.Env = app.Flags.Dir, app.Flags.Env
		opts.Plan = opts.Plan || app.Flags.DryRun
		opts.Out, opts.ErrOut = c.OutOrStdout(), c.ErrOrStderr()
		opts.Confirm = project.Confirm{In: c.InOrStdin(), Out: c.ErrOrStderr(), Interactive: app.UI.Interactive(), Yes: app.Flags.Yes, CI: os.Getenv("CI") != ""}
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
