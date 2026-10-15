package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/deploy"
)

func init() { Register(GroupShip, newRollback) }

func newRollback(app *App) *cobra.Command {
	var o deploy.RollbackOptions
	c := &cobra.Command{Use: "rollback", Short: "Switch to a stored release without changing the database", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		o.Options = deployOptions(app, c)
		return deploy.Rollback(c.Context(), o, deploy.DefaultDeps())
	}}
	c.Flags().StringVar(&o.To, "to", "", "select a stored release ID instead of the previous release")
	c.Flags().BoolVar(&o.WithDBPlan, "with-db-plan", false, "print manual database recovery instructions without restoring data")
	return c
}
