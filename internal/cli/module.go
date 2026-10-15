package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen/newapp"
	"github.com/stopyellingatme/spinup/internal/modules"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/templates/store"
	"github.com/stopyellingatme/spinup/internal/ui"
	"github.com/stopyellingatme/spinup/internal/upgrade"
)

func init() { Register(GroupDevelop, newModule) }

func newModule(app *App) *cobra.Command {
	command := &cobra.Command{Use: "module", Short: "List and enable built-in modules"}
	command.AddCommand(&cobra.Command{
		Use: "list", Short: "List modules and their enabled state", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return modules.List(app.Flags.Dir, app.Flags.JSON, c.OutOrStdout())
		},
	})
	command.AddCommand(newModuleAdd(app))
	return command
}

func newModuleAdd(app *App) *cobra.Command {
	var resume, abort bool
	var options upgrade.Options
	packs, loadErr := newapp.Catalog()
	questions, questionErr := newapp.Questions(packs)
	command := &cobra.Command{
		Use: "add <name>...", Short: "Enable modules on a branch at the locked template version",
		Args: func(_ *cobra.Command, args []string) error {
			if (resume && abort) || ((resume || abort) && len(args) > 0) || (!resume && !abort && len(args) == 0) {
				return &ui.Error{What: "Invalid module arguments", Why: "supply module names, --continue, or --abort", Try: "spinup module add --help", Code: ui.ExitUsage}
			}
			return nil
		},
		RunE: func(c *cobra.Command, args []string) error {
			if loadErr != nil {
				return loadErr
			}
			if questionErr != nil {
				return questionErr
			}
			dir := app.Flags.Dir
			if dir == "" {
				dir = "."
			}
			root, err := project.FindRoot(dir)
			if err != nil {
				return err
			}
			source, err := store.Default(Version, app.Flags.Offline)
			if err != nil {
				return err
			}
			runner := &upgrade.Runner{Root: root, Source: source, Exec: exec.OSRunner{}, Out: c.OutOrStdout(), ErrOut: c.ErrOrStderr(), In: c.InOrStdin(), Interactive: app.UI.Interactive(), CLIVersion: Version}
			options.Yes, options.Offline, options.DryRun = app.Flags.Yes, app.Flags.Offline, app.Flags.DryRun
			if resume {
				return runner.Continue(c.Context(), options)
			}
			if abort {
				return runner.Abort(c.Context(), options)
			}
			answers := map[string]any{}
			for _, q := range questions {
				name := strings.ReplaceAll(q.Key, "_", "-")
				if q.Key == "modules" || q.Key == "app_name" || !c.Flags().Changed(name) {
					continue
				}
				if q.Type == "bool" {
					value, err := c.Flags().GetBool(name)
					if err != nil {
						return err
					}
					answers[q.Key] = value
				} else {
					value, err := c.Flags().GetString(name)
					if err != nil {
						return err
					}
					answers[q.Key] = value
				}
			}
			return runner.AddModules(c.Context(), upgrade.ModuleOptions{Names: args, Answers: answers, Options: options, Prompter: newapp.HuhPrompter{In: c.InOrStdin(), Out: c.ErrOrStderr()}})
		},
	}
	command.Flags().BoolVar(&resume, "continue", false, "continue the recorded module addition")
	command.Flags().BoolVar(&abort, "abort", false, "remove the recorded worktree and branch")
	command.Flags().BoolVar(&options.NoVerify, "no-verify", false, "skip verification in the worktree")
	command.Flags().BoolVar(&options.CommitConflicts, "commit-conflicts", false, "commit conflict markers and continue")
	for _, q := range questions {
		if q.Key == "app_name" || q.Key == "modules" {
			continue
		}
		name := strings.ReplaceAll(q.Key, "_", "-")
		if q.Type == "bool" {
			command.Flags().Bool(name, q.Default == "true", q.Prompt)
		} else {
			command.Flags().String(name, q.Default, q.Prompt)
		}
	}
	return command
}
