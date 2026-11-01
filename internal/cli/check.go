package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/drift"
)

func init() { Register(GroupMaintain, newCheck) }
func newCheck(app *App) *cobra.Command {
	var allowBehind bool
	c := &cobra.Command{Use: "check", Example: `  # Fail when the app drifted from its template (for CI)
  spinup check

  # Accept an app that is behind a newer template version
  spinup check --allow-behind`, Short: "Check application drift for CI", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return drift.Check(c.Context(), driftOptions(app), allowBehind, app.UI)
	}}
	c.Flags().BoolVar(&allowBehind, "allow-behind", false, "allow a newer loadable template version")
	return c
}
