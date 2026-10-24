package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/backup"
)

func init() { registerBackup(newBackupRestore) }

func newBackupRestore(app *App) *cobra.Command {
	var o backup.RestoreOptions
	var label, at string
	c := &cobra.Command{Use: "restore", Short: "Restore a backup into a new or live database", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		target, err := backup.ParseTarget(label, at)
		if err != nil {
			return err
		}
		o.Options = backupOptions(app, c)
		o.Target = target
		o.Plan = o.Plan || app.Flags.DryRun
		o.Confirm = confirmFor(app, c)
		return backup.Restore(c.Context(), o)
	}}
	backupTargetFlags(c, &label, &at)
	f := c.Flags()
	f.StringVar(&o.Into, "into", "", "restore destination: new or in-place")
	f.BoolVar(&o.IKnow, "i-know", false, "allow replacement of the live database")
	f.BoolVar(&o.NoSafetyBackup, "no-safety-backup", false, "skip the safety backup before replacement")
	f.BoolVar(&o.Plan, "plan", false, "print the restore steps and change nothing")
	f.BoolVar(&o.Now, "now", false, "allow restoration outside the maintenance window")
	return c
}
