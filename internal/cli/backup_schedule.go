package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/backup"
)

func init() { registerBackup(newBackupSchedule) }

func newBackupSchedule(app *App) *cobra.Command {
	var plan, remove bool
	command := &cobra.Command{Use: "schedule", Short: "Install or remove backup timers on the VM", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return backup.Schedule(c.Context(), backup.ScheduleOptions{Options: backupOptions(app, c), Plan: plan, Remove: remove})
	}}
	command.Flags().BoolVar(&plan, "plan", false, "print the schedule without changing the VM")
	command.Flags().BoolVar(&remove, "remove", false, "disable and delete backup timers and services")
	return command
}
