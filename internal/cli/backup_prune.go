package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/backup"
)

func init() { registerBackup(newBackupPrune) }

func newBackupPrune(app *App) *cobra.Command {
	var plan bool
	c := &cobra.Command{Use: "prune", Short: "Apply backup retention and list the remaining backups", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		_, err := backup.Prune(c.Context(), backup.PruneOptions{Options: backupOptions(app, c), Plan: plan || app.Flags.DryRun})
		return err
	}}
	c.Flags().BoolVar(&plan, "plan", false, "preview retention without removing backups")
	return c
}
