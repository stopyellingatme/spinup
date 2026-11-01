package cli

import (
	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/deploy"
)

func init() {
	RegisterSecrets(func(app *App) *cobra.Command {
		var acceptNewHostKey bool
		cmd := &cobra.Command{
			Use: "push",
			Example: `  # Write the decrypted secrets to the host
  spinup secrets push --env production

  # Trust the host key on the first connection
  spinup secrets push --accept-new-host-key --env production`,
			Short: "Write the environment's decrypted secrets to the host, under the deploy lock",
			Args:  cobra.NoArgs,
			RunE: func(c *cobra.Command, _ []string) error {
				opts := deployOptions(app, c)
				opts.AcceptNewHostKey = acceptNewHostKey
				return deploy.PushSecrets(c.Context(), opts, deploy.DefaultDeps())
			},
		}
		cmd.Flags().BoolVar(&acceptNewHostKey, "accept-new-host-key", false, "trust the host key on the first connection")
		return cmd
	})
}
