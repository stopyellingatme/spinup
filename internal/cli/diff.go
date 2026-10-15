package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/drift"
)

func driftOptions(app *App) drift.Options {
	return drift.Options{Dir: app.Flags.Dir, Version: Version, Offline: app.Flags.Offline, JSON: app.Flags.JSON, DryRun: app.Flags.DryRun}
}
func init() { Register(GroupMaintain, newDiff) }
func newDiff(app *App) *cobra.Command {
	return &cobra.Command{Use: "diff [path...]", Short: "Compare tracked files with their templates", RunE: func(c *cobra.Command, args []string) error {
		return drift.Diff(c.Context(), driftOptions(app), args, app.UI)
	}}
}
