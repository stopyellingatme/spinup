package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/db/ops"
	exec "github.com/stopyellingatme/spinup/internal/exec"
)

func init() { registerDB(newDBPsql) }

func newDBPsql(app *App) *cobra.Command {
	return &cobra.Command{Use: "psql", Short: "Open psql on the development database", Args: cobra.NoArgs, RunE: func(c *cobra.Command, args []string) error {
		return ops.Psql(c.Context(), dbOptions(app, c, false), exec.OSRunner{})
	}}
}
