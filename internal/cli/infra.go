package cli

import (
	"os"

	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/infra"
	"github.com/stopyellingatme/spinup/internal/project"
)

func init() { Register(GroupShip, newInfra) }

func newInfra(app *App) *cobra.Command {
	group := &cobra.Command{Use: "infra", Short: "Provision Hetzner Cloud infrastructure with OpenTofu", Args: cobra.NoArgs}
	for _, item := range []struct{ name, short string }{
		{"plan", "Preview infrastructure changes"},
		{"apply", "Apply infrastructure changes and update the environment host"},
		{"output", "Show infrastructure outputs"},
		{"destroy", "Destroy the environment infrastructure"},
	} {
		var know bool
		c := &cobra.Command{Use: item.name, Short: item.short, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
			return infra.Run(c.Context(), infra.Options{Dir: app.Flags.Dir, Env: app.Flags.Env, Action: c.Name(), IKnow: know, DryRun: app.Flags.DryRun, JSON: app.Flags.JSON, Out: c.OutOrStdout(), ErrOut: c.ErrOrStderr(), Confirm: project.Confirm{In: c.InOrStdin(), Out: c.ErrOrStderr(), Interactive: app.UI.Interactive(), Yes: app.Flags.Yes, CI: os.Getenv("CI") != ""}}, infra.Deps{})
		}}
		if item.name == "destroy" {
			c.Flags().BoolVar(&know, "i-know", false, "confirm destruction of the server and volume")
		}
		group.AddCommand(c)
	}
	return group
}
