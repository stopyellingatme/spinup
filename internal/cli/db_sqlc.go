package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/db/ops"
	exec "github.com/stopyellingatme/spinup/internal/exec"
)

func init() { registerDB(newDBSqlc) }

func newDBSqlc(app *App) *cobra.Command {
	return &cobra.Command{Use: "sqlc", Short: "Generate the Go query code with sqlc", Args: cobra.NoArgs, RunE: func(c *cobra.Command, args []string) error {
		return ops.Sqlc(c.Context(), dbOptions(app, c, false), exec.OSRunner{})
	}}
}
