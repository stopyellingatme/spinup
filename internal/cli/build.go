package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/build"
	exec "github.com/stopyellingatme/spinup/internal/exec"
)

func init() { Register(GroupShip, newBuild) }

func newBuild(app *App) *cobra.Command {
	var o build.Options
	c := &cobra.Command{Use: "build", Short: "Build the application API", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		o.Dir = app.Flags.Dir
		o.DryRun = app.Flags.DryRun
		o.Out = c.OutOrStdout()
		o.ErrOut = c.ErrOrStderr()
		return build.Local(c.Context(), o, exec.OSRunner{})
	}}
	c.Flags().StringVar(&o.Target, "target", "api", "target to build: api, web, or all")
	c.Flags().StringVar(&o.Tag, "tag", "", "version tag (default dev)")
	c.Flags().BoolVar(&o.Image, "image", false, "build the API container image")
	return c
}
