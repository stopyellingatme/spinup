package cli

import "github.com/spf13/cobra"

func init() { Register(GroupData, newDB) }

func newDB(app *App) *cobra.Command {
	c := &cobra.Command{Use: "db", Short: "Operate the application database", Args: cobra.NoArgs}
	c.AddCommand(newDBMigrate(app))
	return c
}
