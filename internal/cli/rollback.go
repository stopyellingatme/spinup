package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/deploy"
)

func init() { Register(GroupShip, newRollback) }

func newRollback(app *App) *cobra.Command {
	var o deploy.RollbackOptions
	c := &cobra.Command{Use: "rollback", Example: `  # Go back to the previous release
  spinup rollback --env production

  # Go back to a chosen release
  spinup rollback --to 20261101T093000Z-ab12cd3 --env production

  # Print the steps to recover the database by hand
  spinup rollback --with-db-plan --env production`, Short: "Switch to a stored release without changing the database", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		o.Options = deployOptions(app, c)
		return deploy.Rollback(c.Context(), o, deploy.DefaultDeps())
	}}
	c.Flags().StringVar(&o.To, "to", "", "select a stored release ID instead of the previous release")
	c.Flags().BoolVar(&o.WithDBPlan, "with-db-plan", false, "print manual database recovery instructions without restoring data")
	return c
}
