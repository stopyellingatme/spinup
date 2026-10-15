package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/drift"
	"github.com/stopyellingatme/spinup/internal/exec"
)

func init() { Register(GroupMaintain, newDoctor) }
func newDoctor(app *App) *cobra.Command {
	var quick bool
	c := &cobra.Command{Use: "doctor", Short: "Check local application health", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return drift.Doctor(c.Context(), driftOptions(app), quick, exec.OSRunner{}, app.UI)
	}}
	c.Flags().BoolVar(&quick, "quick", false, "run only quick checks")
	return c
}
