package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/db/ops"
	exec "github.com/stopyellingatme/spinup/internal/exec"
)

func init() { registerDB(newDBSeed) }

func newDBSeed(app *App) *cobra.Command {
	return &cobra.Command{Use: "seed", Short: "Run the seed files in the development database", Args: cobra.NoArgs, RunE: func(c *cobra.Command, args []string) error {
		return ops.Seed(c.Context(), dbOptions(app, c, false), exec.OSRunner{})
	}}
}
