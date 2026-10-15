package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/backup"
)

func init() { Register(GroupOperate, newBackup) }

// backupSubcommands holds the builders of the backup subcommands that register with registerBackup.
var backupSubcommands []func(app *App) *cobra.Command

// registerBackup adds a subcommand to `spinup backup`. Call it from init().
func registerBackup(build func(app *App) *cobra.Command) {
	regMu.Lock()
	defer regMu.Unlock()
	backupSubcommands = append(backupSubcommands, build)
}

// The M7 units register their backup subcommands; this keeps the pre-seed lint-clean.
var _ = registerBackup

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
	for _, build := range backupSubcommands {
		group.AddCommand(build(app))
	}
	return group
}
