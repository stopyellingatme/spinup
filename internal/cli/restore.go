package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/drift"
)

func init() { Register(GroupMaintain, newRestore) }
func newRestore(app *App) *cobra.Command {
	var force bool
	c := &cobra.Command{Use: "restore <path>", Short: "Restore a tracked file from its template", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		return drift.Restore(c.Context(), driftOptions(app), args[0], force)
	}}
	c.Flags().BoolVar(&force, "force", false, "replace an existing edited file")
	return c
}
