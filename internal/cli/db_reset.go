package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/db/ops"
	exec "github.com/stopyellingatme/spinup/internal/exec"
)

func init() { registerDB(newDBReset) }

func newDBReset(app *App) *cobra.Command {
	return &cobra.Command{Use: "reset", Short: "Recreate the development database and migrate up", Args: cobra.NoArgs, RunE: func(c *cobra.Command, args []string) error {
		return ops.Reset(c.Context(), dbOptions(app, c, false), exec.OSRunner{})
	}}
}
