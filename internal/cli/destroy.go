package cli

import (
	"github.com/spf13/cobra"
	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen/destroy"
	"github.com/stopyellingatme/spinup/internal/project"
)

func init() { Register(GroupDevelop, newDestroy) }

func newDestroy(app *App) *cobra.Command {
	c := &cobra.Command{Use: "destroy <kind> <name>", Short: "Remove unchanged generated files", Args: cobra.ExactArgs(2)}
	c.RunE = func(c *cobra.Command, args []string) error {
		root, err := project.FindRoot(app.Flags.Dir)
		if err != nil {
			return err
		}
		return destroy.Run(c.Context(), destroy.Options{Root: root, Kind: args[0], Name: args[1], DryRun: app.Flags.DryRun, Out: c.OutOrStdout()}, exec.OSRunner{})
	}
	return c
}
