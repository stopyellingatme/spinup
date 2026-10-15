package cli

import (
	"github.com/spf13/cobra"
	remotecheck "github.com/stopyellingatme/spinup/internal/doctor/remote"
	"github.com/stopyellingatme/spinup/internal/drift"
	"github.com/stopyellingatme/spinup/internal/exec"
	pluginrpc "github.com/stopyellingatme/spinup/internal/plugin/rpc"
	"github.com/stopyellingatme/spinup/internal/project"
)

func init() { Register(GroupMaintain, newDoctor) }
func newDoctor(app *App) *cobra.Command {
	var quick bool
	var remote bool
	c := &cobra.Command{Use: "doctor", Short: "Check local application health", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if remote {
			return remotecheck.Doctor(c.Context(), remotecheck.Options{Dir: app.Flags.Dir, Env: app.Flags.Env, JSON: app.Flags.JSON, Out: c.OutOrStdout()})
		}
		if root, err := project.FindRoot(app.Flags.Dir); err == nil {
			if err := pluginrpc.RegisterProjectChecks(c.Context(), pluginrpc.Options{Root: root, Offline: app.Flags.Offline, Stderr: c.ErrOrStderr()}); err != nil {
				return err
			}
		}
		return drift.Doctor(c.Context(), driftOptions(app), quick, exec.OSRunner{}, app.UI)
	}}
	c.Flags().BoolVar(&quick, "quick", false, "run only quick checks")
	c.Flags().BoolVar(&remote, "remote", false, "run checks on the deployed VM")
	return c
}
