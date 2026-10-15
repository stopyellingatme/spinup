package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/backup"
)

func init() { Register(GroupOperate, newBackup) }

func backupOptions(app *App, c *cobra.Command) backup.Options {
	return backup.Options{Dir: app.Flags.Dir, Env: app.Flags.Env, JSON: app.Flags.JSON, Out: c.OutOrStdout(), ErrOut: c.ErrOrStderr()}
}

func newBackup(app *App) *cobra.Command {
	group := &cobra.Command{Use: "backup", Short: "Back up the production database with pgBackRest", Args: cobra.NoArgs}
	var kind string
	now := &cobra.Command{Use: "now", Short: "Take a backup now and print its label, size, and duration", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		k, err := backup.ParseKind(kind)
		if err != nil {
			return err
		}
		o := backupOptions(app, c)
		o.Kind = k
		_, err = backup.Now(c.Context(), o)
		return err
	}}
	now.Flags().StringVar(&kind, "type", "diff", "backup type: full, diff, or incr")
	group.AddCommand(now)
	group.AddCommand(&cobra.Command{Use: "list", Short: "List the backups in the repository", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		_, err := backup.List(c.Context(), backupOptions(app, c))
		return err
	}})
	group.AddCommand(&cobra.Command{Use: "status", Short: "Show the repository status and the last backup", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		_, err := backup.ShowStatus(c.Context(), backupOptions(app, c))
		return err
	}})
	return group
}
