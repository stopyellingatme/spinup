package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/templates/store"
	"github.com/stopyellingatme/spinup/internal/ui"
	"github.com/stopyellingatme/spinup/internal/upgrade"
)

func init() { Register(GroupMaintain, newUpgrade) }

func newUpgrade(app *App) *cobra.Command {
	var options upgrade.Options
	var plan, resume, abort bool
	command := &cobra.Command{
		Use: "upgrade", Short: "Upgrade app templates in a separate Git worktree",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return &ui.Error{What: "Invalid upgrade arguments", Why: "upgrade accepts flags only", Try: "spinup upgrade --help", Code: ui.ExitUsage}
			}
			selected := 0
			for _, flag := range []bool{plan, resume, abort} {
				if flag {
					selected++
				}
			}
			if selected > 1 {
				return &ui.Error{What: "Invalid upgrade flags", Why: "--plan, --continue, and --abort are mutually exclusive", Try: "select only one upgrade mode", Code: ui.ExitUsage}
			}
			return nil
		},
		RunE: func(c *cobra.Command, _ []string) error {
			dir := app.Flags.Dir
			if dir == "" {
				var err error
				dir, err = os.Getwd()
				if err != nil {
					return &ui.Error{What: "Cannot locate project", Why: err.Error(), Try: "pass --dir <project>", Err: err}
				}
			}
			root, err := project.FindRoot(dir)
			if err != nil {
				return err
			}
			source, err := store.Default(Version, app.Flags.Offline)
			if err != nil {
				return err
			}
			executor := exec.OSRunner{}
			runner := &upgrade.Runner{Root: root, Source: source, Merger: upgrade.NewMerger(executor), Exec: executor, Out: c.OutOrStdout(), ErrOut: c.ErrOrStderr(), In: c.InOrStdin(), Interactive: app.UI.Interactive(), CLIVersion: Version}
			options.Yes = app.Flags.Yes
			options.Offline = app.Flags.Offline
			options.DryRun = app.Flags.DryRun
			switch {
			case plan || (options.DryRun && !abort && !resume):
				_, err = runner.Plan(c.Context(), options)
				return err
			case resume:
				return runner.Continue(c.Context(), options)
			case abort:
				return runner.Abort(c.Context(), options)
			default:
				return runner.Run(c.Context(), options)
			}
		},
	}
	flags := command.Flags()
	flags.StringVar(&options.To, "to", "", "target template version (default newest loadable)")
	flags.BoolVar(&plan, "plan", false, "print the plan without changing project files")
	flags.BoolVar(&resume, "continue", false, "continue an upgrade after resolving conflicts")
	flags.BoolVar(&abort, "abort", false, "remove an upgrade worktree and branch")
	flags.BoolVar(&options.AllowDirty, "allow-dirty", false, "allow changes in the main checkout")
	flags.BoolVar(&options.ForceManaged, "force-managed", false, "overwrite changed managed files")
	flags.BoolVar(&options.NoVerify, "no-verify", false, "skip verification after the upgrade")
	flags.BoolVar(&options.CommitConflicts, "commit-conflicts", false, "commit conflict markers and continue")
	for _, extension := range upgrade.Extensions() {
		if extension.Flags != nil {
			extension.Flags(flags)
		}
	}
	return command
}
