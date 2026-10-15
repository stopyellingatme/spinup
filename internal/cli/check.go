package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/drift"
)

func init() { Register(GroupMaintain, newCheck) }
func newCheck(app *App) *cobra.Command {
	var allowBehind bool
	c := &cobra.Command{Use: "check", Short: "Check application drift for CI", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return drift.Check(c.Context(), driftOptions(app), allowBehind, app.UI)
	}}
	c.Flags().BoolVar(&allowBehind, "allow-behind", false, "allow a newer loadable template version")
	return c
}
