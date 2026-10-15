package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/apptest"
	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
)

func init() { Register(GroupDevelop, newTest) }

func newTest(app *App) *cobra.Command {
	var o apptest.Options
	var api bool
	c := &cobra.Command{Use: "test", Short: "Run application tests", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if !o.Web && !o.E2E && !o.Contract {
			root, err := project.FindRoot(app.Flags.Dir)
			if err != nil {
				return err
			}
			o.Root = root
		}
		o.DryRun = app.Flags.DryRun
		o.Out = c.OutOrStdout()
		o.ErrOut = c.ErrOrStderr()
		return apptest.Run(c.Context(), o, exec.OSRunner{})
	}}
	c.Flags().BoolVar(&api, "api", true, "run API tests (default)")
	c.Flags().StringVar(&o.Run, "run", "", "run tests matching this regular expression")
	c.Flags().BoolVar(&o.Race, "race", false, "enable the Go race detector")
	c.Flags().BoolVar(&o.Cover, "cover", false, "report Go test coverage")
	c.Flags().BoolVar(&o.Web, "web", false, "run web tests (M4)")
	c.Flags().BoolVar(&o.E2E, "e2e", false, "run end-to-end tests (M4)")
	c.Flags().BoolVar(&o.Contract, "contract", false, "run contract tests (M4)")
	return c
}
