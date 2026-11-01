package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/db/ops"
)

func init() { registerDB(newDBNew) }

func newDBNew(app *App) *cobra.Command {
	return &cobra.Command{Use: "new <name>", Example: `  # Create an empty migration, then edit the SQL
  spinup db new add_ticket_index`, Short: "Create an empty SQL migration", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		return ops.New(dbOptions(app, c, false), args[0])
	}}
}
