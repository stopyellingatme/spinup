package cli

import (
	"github.com/spf13/cobra"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/server"
)

func init() { Register(GroupOperate, newServer) }

func newServer(app *App) *cobra.Command {
	var accept bool
	group := &cobra.Command{Use: "server", Example: `  # Prepare a new VM, then audit it
  spinup server bootstrap --env production
  spinup server audit --env production`, Short: "Prepare, audit, and access a VM", Args: cobra.NoArgs}
	group.PersistentFlags().BoolVar(&accept, "accept-new-host-key", false, "accept a previously unknown SSH host key")
	options := func(c *cobra.Command) server.Options {
		return server.Options{Dir: app.Flags.Dir, Env: app.Flags.Env, JSON: app.Flags.JSON, AcceptNewHostKey: accept, In: c.InOrStdin(), Out: c.OutOrStdout(), ErrOut: c.ErrOrStderr()}
	}
	var key string
	var plan, cloudInit bool
	bootstrap := &cobra.Command{Use: "bootstrap", Example: `  # Preview the changes
  spinup server bootstrap --plan --env production

  # Prepare the host with your key
  spinup server bootstrap --ssh-key ~/.ssh/id_ed25519.pub --env production

  # Print the cloud-init instead
  spinup server bootstrap --print-cloud-init --env production`, Short: "Prepare a Debian or Ubuntu host", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		o := options(c)
		o.SSHKey, o.Plan, o.PrintCloudInit = key, plan || app.Flags.DryRun, cloudInit
		return server.Bootstrap(c.Context(), o, runexec.OSRunner{})
	}}
	bootstrap.Flags().StringVar(&key, "ssh-key", "~/.ssh/id_ed25519.pub", "operator public key for the deploy user")
	bootstrap.Flags().BoolVar(&plan, "plan", false, "probe the host and print changes without applying them")
	bootstrap.Flags().BoolVar(&cloudInit, "print-cloud-init", false, "print the rendered cloud-init with the operator public key")
	group.AddCommand(bootstrap)
	group.AddCommand(&cobra.Command{Use: "audit", Example: `  # Report unsafe settings on the host
  spinup server audit --env production`, Short: "Report unsafe settings on the host", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return server.Audit(c.Context(), options(c), runexec.OSRunner{})
	}})
	var follow bool
	var since string
	logs := &cobra.Command{Use: "logs <service>", Example: `  # Show the logs of the api service
  spinup server logs api --env production

  # Follow the logs of all replicas
  spinup server logs api --follow --env production

  # Show the last 10 minutes
  spinup server logs api --since 10m --env production`, Short: "Read service logs from the host", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		o := options(c)
		o.Follow, o.Since = follow, since
		return server.Logs(c.Context(), o, args[0], runexec.OSRunner{})
	}}
	logs.Flags().BoolVar(&follow, "follow", false, "follow logs from every running replica")
	logs.Flags().StringVar(&since, "since", "", "show logs since a timestamp or duration, such as 10m")
	group.AddCommand(logs)
	group.AddCommand(&cobra.Command{Use: "ssh", Example: `  # Open a shell on the host
  spinup server ssh --env production`, Short: "Open an interactive shell on the host", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return server.SSH(c.Context(), options(c), runexec.OSRunner{})
	}})
	return group
}
