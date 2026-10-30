package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/build"
	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func init() { Register(GroupShip, newBuild) }

func newBuild(app *App) *cobra.Command {
	var o build.Options
	c := &cobra.Command{Use: "build", Short: "Build the application API", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if o.Image {
			return buildImagesCommand(c.Context(), app, c, o)
		}
		if len(o.ImageTargets) > 1 {
			return &ui.Error{What: "Cannot build target", Why: "local builds accept one target", Try: "pass one --target, or use --image for multiple targets"}
		}
		if len(o.ImageTargets) == 1 {
			o.Target = o.ImageTargets[0]
		}
		o.Dir = app.Flags.Dir
		o.DryRun = app.Flags.DryRun
		o.Out = c.OutOrStdout()
		o.ErrOut = c.ErrOrStderr()
		return build.Local(c.Context(), o, exec.OSRunner{})
	}}
	c.Flags().StringSliceVar(&o.ImageTargets, "target", nil, "image target to build; repeat or separate names with commas")
	c.Flags().StringVar(&o.Tag, "tag", "", "version tag (default dev)")
	c.Flags().BoolVar(&o.Image, "image", false, "build application container images")
	registerBuildImageFlags(c, &o)
	return c
}
