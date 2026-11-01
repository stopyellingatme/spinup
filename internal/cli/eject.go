package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/drift"
)

func init() { Register(GroupMaintain, newEject) }
func newEject(app *App) *cobra.Command {
	return &cobra.Command{Use: "eject <path>", Example: `  # Take ownership of a managed file so upgrades leave it alone
  spinup eject web/src/routes/+layout.svelte`, Short: "Take ownership of a managed file", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		return drift.Eject(c.Context(), driftOptions(app), args[0])
	}}
}
