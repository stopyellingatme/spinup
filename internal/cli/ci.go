package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/ci"
	genrun "github.com/stopyellingatme/spinup/internal/gen/run"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/templates/store"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func init() { Register(GroupMaintain, newCI) }

func newCI(app *App) *cobra.Command {
	command := &cobra.Command{Use: "ci", Short: "Generate and verify GitHub Actions files"}
	var diff, force bool
	generate := &cobra.Command{Use: "generate", Short: "Render the locked ci pack", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		root, err := project.FindRoot(app.Flags.Dir)
		if err != nil {
			return err
		}
		source, err := store.Default(Version, app.Flags.Offline)
		if err != nil {
			return err
		}
		_, err = ci.Generate(c.Context(), source, genrun.Options{Root: root, DryRun: app.Flags.DryRun, Diff: diff, Force: force, JSON: app.Flags.JSON, Out: c.OutOrStdout()})
		return err
	}}
	generate.Flags().BoolVar(&diff, "diff", false, "show unified diffs without writing")
	generate.Flags().BoolVar(&force, "force", false, "overwrite workflows with user changes")
	verify := &cobra.Command{Use: "verify", Short: "Check workflow safety and run optional actionlint", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		root, err := project.FindRoot(app.Flags.Dir)
		if err != nil {
			return err
		}
		report, err := ci.Verify(c.Context(), ci.VerifyOptions{Root: root})
		if err != nil {
			return err
		}
		if err := report.Write(c.OutOrStdout(), app.Flags.JSON); err != nil {
			return fmt.Errorf("write CI report: %w\nTry: check the output destination", err)
		}
		if len(report.Findings) > 0 {
			return &ui.Error{What: "CI verification failed", Why: fmt.Sprintf("%d finding(s)", len(report.Findings)), Try: "correct the findings, then run spinup ci verify", Code: ui.ExitError}
		}
		return nil
	}}
	command.AddCommand(generate, verify)
	return command
}
