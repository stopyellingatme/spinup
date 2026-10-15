package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/db/ops"
	exec "github.com/stopyellingatme/spinup/internal/exec"
)

func init() { registerDB(newDBDoctor) }

func newDBDoctor(app *App) *cobra.Command {
	return &cobra.Command{Use: "doctor", Short: "Check the PostgreSQL version and pending migrations", Args: cobra.NoArgs, RunE: func(c *cobra.Command, args []string) error {
		return ops.Doctor(c.Context(), dbOptions(app, c, false), exec.OSRunner{})
	}}
}
