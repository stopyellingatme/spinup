package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/consoleclient"
	"github.com/stopyellingatme/spinup/internal/exec"
)

func init() { Register(GroupOperate, newStatus) }

func newStatus(app *App) *cobra.Command {
	var accept bool
	c := &cobra.Command{Use: "status", Example: `  # Show the overview of production
  spinup status --env production

  # Write the overview document as JSON
  spinup status --json --env production`, Short: "Show the console overview of a deployed app", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		o := consoleclient.Options{Dir: app.Flags.Dir, Env: app.Flags.Env, JSON: app.Flags.JSON, AcceptNewHostKey: accept, Out: c.OutOrStdout(), ErrOut: c.ErrOrStderr()}
		return consoleclient.Status(c.Context(), o, exec.OSRunner{})
	}}
	c.Flags().BoolVar(&accept, "accept-new-host-key", false, "accept a previously unknown SSH host key")
	return c
}
