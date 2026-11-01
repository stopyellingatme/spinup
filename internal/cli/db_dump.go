package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/db/ops"
	exec "github.com/stopyellingatme/spinup/internal/exec"
)

func init() { registerDB(newDBDump) }

func newDBDump(app *App) *cobra.Command {
	return &cobra.Command{Use: "dump [file]", Example: `  # Dump the development database to the default file
  spinup db dump

  # Dump to a named file
  spinup db dump backup.sql`, Short: "Dump the development database (default " + ops.DefaultDumpFile + ")", Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		file := ""
		if len(args) > 0 {
			file = args[0]
		}
		return ops.Dump(c.Context(), dbOptions(app, c, false), exec.OSRunner{}, file)
	}}
}
