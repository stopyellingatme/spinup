package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/db/ops"
	exec "github.com/stopyellingatme/spinup/internal/exec"
)

func init() { registerDB(newDBTo) }

func newDBTo(app *App) *cobra.Command {
	var know bool
	c := &cobra.Command{Use: "to <version>", Short: "Migrate up or down to a version", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		return ops.To(c.Context(), dbOptions(app, c, know), exec.OSRunner{}, args[0])
	}}
	c.Flags().BoolVar(&know, "i-know", false, "acknowledge that a move down can lose data")
	return c
}
