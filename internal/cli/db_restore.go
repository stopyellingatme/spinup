package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/db/ops"
	exec "github.com/stopyellingatme/spinup/internal/exec"
)

func init() { registerDB(newDBRestore) }

func newDBRestore(app *App) *cobra.Command {
	return &cobra.Command{Use: "restore <file>", Short: "Restore a dump into the development database", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		return ops.Restore(c.Context(), dbOptions(app, c, false), exec.OSRunner{}, args[0])
	}}
}
