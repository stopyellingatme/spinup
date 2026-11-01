package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/backup"
)

func init() { registerBackup(newBackupVerify) }

func backupTargetFlags(c *cobra.Command, label, at *string) {
	c.Flags().StringVar(label, "label", "", "backup label (default latest)")
	c.Flags().StringVar(at, "time", "", "point in time in RFC 3339 format")
}

func newBackupVerify(app *App) *cobra.Command {
	var label, at string
	c := &cobra.Command{Use: "verify", Example: `  # Restore the latest backup in isolation and check it
  spinup backup verify

  # Check the state at a point in time
  spinup backup verify --time 2026-10-31T14:00:00Z`, Short: "Restore a backup in isolation and check the database", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		target, err := backup.ParseTarget(label, at)
		if err != nil {
			return err
		}
		_, err = backup.Verify(c.Context(), backupOptions(app, c), target)
		return err
	}}
	backupTargetFlags(c, &label, &at)
	return c
}
