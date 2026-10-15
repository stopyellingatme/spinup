package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/routes"
)

func init() { Register(GroupDevelop, newRoutes) }

func newRoutes(app *App) *cobra.Command {
	return &cobra.Command{Use: "routes", Short: "List application HTTP routes", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		root, err := project.FindRoot(app.Flags.Dir)
		if err != nil {
			return err
		}
		list, err := routes.List(root)
		if err != nil {
			return err
		}
		return routes.Print(c.OutOrStdout(), list, app.Flags.JSON)
	}}
}
