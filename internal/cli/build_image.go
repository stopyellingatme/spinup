package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/build"
	exec "github.com/stopyellingatme/spinup/internal/exec"
)

func registerBuildImageFlags(c *cobra.Command, o *build.Options) {
	c.Flags().BoolVar(&o.Push, "push", false, "push images with provenance and an SBOM")
	c.Flags().StringVar(&o.Platform, "platform", "", "target platform (default linux/amd64)")
	c.Flags().StringVar(&o.Registry, "registry", "", "container registry (default ghcr.io)")
}

func buildImagesCommand(ctx context.Context, app *App, c *cobra.Command, o build.Options) error {
	o.Dir = app.Flags.Dir
	o.Env = app.Flags.Env
	o.DryRun = app.Flags.DryRun
	o.Out = c.OutOrStdout()
	o.ErrOut = c.ErrOrStderr()
	images, err := build.Images(ctx, o, exec.OSRunner{})
	if err != nil {
		return err
	}
	if o.DryRun {
		return nil
	}
	if app.Flags.JSON {
		return app.UI.JSON(images)
	}
	if !o.Push {
		fmt.Fprintln(o.Out, "Attestations require --push and were not created.")
	}
	fmt.Fprintln(o.Out, "NAME\tREF\tDIGEST")
	for _, image := range images {
		fmt.Fprintf(o.Out, "%s\t%s\t%s\n", image.Name, image.Ref, image.Digest)
	}
	return nil
}
