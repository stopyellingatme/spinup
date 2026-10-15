package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/db/ops"
	exec "github.com/stopyellingatme/spinup/internal/exec"
)

func init() { registerDB(newDBRedo) }

func newDBRedo(app *App) *cobra.Command {
	var know bool
	c := &cobra.Command{Use: "redo", Short: "Roll back the last migration and apply it again", Args: cobra.NoArgs, RunE: func(c *cobra.Command, args []string) error {
		return ops.Redo(c.Context(), dbOptions(app, c, know), exec.OSRunner{})
	}}
	c.Flags().BoolVar(&know, "i-know", false, "acknowledge that redo can lose data")
	return c
}
