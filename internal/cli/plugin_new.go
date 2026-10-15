package cli

import (
	"runtime"

	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/plugin/scaffold"
)

func init() { registerPlugin(newPluginNew) }

func newPluginNew(app *App) *cobra.Command {
	var kind, out, sdkPath, platform string
	command := &cobra.Command{
		Use: "new <name>", Short: "Create a declarative or executable plugin project", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			dir, err := scaffold.Create(scaffold.Options{Dir: app.Flags.Dir, Name: args[0], Kind: kind, Platform: platform, Out: out, SDKPath: sdkPath, DryRun: app.Flags.DryRun})
			if err != nil {
				return err
			}
			if app.Flags.JSON {
				return app.UI.JSON(struct {
					Path string `json:"path"`
					Kind string `json:"kind"`
				}{dir, kind})
			}
			if app.Flags.DryRun {
				app.UI.Printf("Would create %s plugin in %s\n", kind, dir)
			} else {
				app.UI.Printf("Created %s plugin in %s\n", kind, dir)
			}
			return nil
		},
	}
	command.Flags().StringVar(&kind, "kind", "pack", "plugin kind: pack or exec")
	command.Flags().StringVar(&out, "out", "", "destination directory (default ./<name>)")
	command.Flags().StringVar(&platform, "platform", runtime.GOOS+"-"+runtime.GOARCH, "executable target as GOOS-GOARCH")
	command.Flags().StringVar(&sdkPath, "sdk-path", "", "local plugin-sdk directory for an executable plugin")
	return command
}
