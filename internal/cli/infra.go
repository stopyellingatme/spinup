package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/infra"
)

func init() { Register(GroupShip, newInfra) }

func newInfra(app *App) *cobra.Command {
	group := &cobra.Command{Use: "infra", Example: `  # Preview changes, apply them, then read the outputs
  spinup infra plan --env production
  spinup infra apply --env production
  spinup infra output --env production`, Short: "Provision Hetzner Cloud infrastructure with OpenTofu", Args: cobra.NoArgs}
	for _, item := range []struct{ name, short, example string }{
		{"plan", "Preview infrastructure changes", "  # Preview what OpenTofu would change\n  spinup infra plan --env production"},
		{"apply", "Apply infrastructure changes and update the environment host", "  # Create or update the server and volume\n  spinup infra apply --env production\n\n  # Show what would happen without changes\n  spinup infra apply --dry-run --env production"},
		{"output", "Show infrastructure outputs", "  # Show the host address and other outputs\n  spinup infra output --env production\n\n  # Print them as JSON\n  spinup infra output --json --env production"},
		{"destroy", "Destroy the environment infrastructure", "  # Delete the server and volume (this removes data)\n  spinup infra destroy --i-know --env staging"},
	} {
		var know bool
		c := &cobra.Command{Use: item.name, Short: item.short, Example: item.example, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
			return infra.Run(c.Context(), infra.Options{Dir: app.Flags.Dir, Env: app.Flags.Env, Action: c.Name(), IKnow: know, DryRun: app.Flags.DryRun, JSON: app.Flags.JSON, Out: c.OutOrStdout(), ErrOut: c.ErrOrStderr(), Confirm: confirmFor(app, c)}, infra.Deps{})
		}}
		if item.name == "destroy" {
			c.Flags().BoolVar(&know, "i-know", false, "confirm destruction of the server and volume")
		}
		group.AddCommand(c)
	}
	return group
}
