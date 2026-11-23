package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/consoleclient"
	"github.com/stopyellingatme/spinup/internal/exec"
)

func init() { Register(GroupOperate, newConsole) }

func newConsole(app *App) *cobra.Command {
	group := &cobra.Command{Use: "console", Example: `  # Open the operator console of production
  spinup console open --env production`, Short: "Open the operator console through SSH", Args: cobra.NoArgs}
	var noBrowser, accept bool
	open := &cobra.Command{Use: "open", Example: `  # Open the console in the browser through an SSH tunnel
  spinup console open --env production

  # Print the URL and do not open a browser
  spinup console open --no-browser --env production`, Short: "Open the console through local SSH tunnels", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		o := consoleclient.Options{Dir: app.Flags.Dir, Env: app.Flags.Env, JSON: app.Flags.JSON, NoBrowser: noBrowser, AcceptNewHostKey: accept, Out: c.OutOrStdout(), ErrOut: c.ErrOrStderr()}
		return consoleclient.Open(c.Context(), o, exec.OSRunner{})
	}}
	open.Flags().BoolVar(&noBrowser, "no-browser", false, "print the URL without opening a browser")
	open.Flags().BoolVar(&accept, "accept-new-host-key", false, "accept a previously unknown SSH host key")
	group.AddCommand(open)
	return group
}
