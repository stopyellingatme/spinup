package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	genregistry "github.com/stopyellingatme/spinup/internal/gen/registry"
	genrun "github.com/stopyellingatme/spinup/internal/gen/run"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func init() { Register(GroupDevelop, newGenerate) }

func newGenerate(app *App) *cobra.Command {
	return generateCommand(app, genregistry.All(), nil, app.UI.Interactive)
}

func generateCommand(app *App, kinds []gen.Generator, p genrun.Prompter, interactive func() bool) *cobra.Command {
	var diff, force bool
	c := &cobra.Command{
		Use: "generate <kind> [args...]", Aliases: []string{"g"}, Short: "Generate application files",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				return &ui.Error{What: "Cannot generate files", Why: fmt.Sprintf("generator %q is not registered", args[0]), Try: "spinup g <kind> --help", Code: ui.ExitUsage}
			}
			return nil
		},
	}
	c.PersistentFlags().BoolVar(&diff, "diff", false, "show unified diffs without writing")
	c.PersistentFlags().BoolVar(&force, "force", false, "overwrite files with different content")
	children := make(map[string]*cobra.Command, len(kinds))
	for _, g := range kinds {
		child := &cobra.Command{Use: g.Name, Aliases: g.Aliases, Short: g.Short}
		if g.Usage != "" {
			child.Use += " " + g.Usage
		}
		if g.Flags != nil {
			g.Flags(child.Flags())
		}
		child.RunE = func(cmd *cobra.Command, args []string) error {
			root, err := project.FindRoot(app.Flags.Dir)
			if err != nil {
				return &ui.Error{What: "Cannot find the project", Why: err.Error(), Try: "run inside a spinup project, or pass --dir", Err: err}
			}
			_, err = genrun.Run(cmd.Context(), g, args, cmd.Flags(), genrun.Options{Root: root, DryRun: app.Flags.DryRun, Diff: diff, Force: force, JSON: app.Flags.JSON, Out: cmd.OutOrStdout(), Runner: exec.OSRunner{}})
			return err
		}
		children[g.Name] = child
		c.AddCommand(child)
	}
	c.RunE = func(cmd *cobra.Command, _ []string) error {
		if !interactive() {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Available generators:")
			for _, g := range kinds {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s\t%s\n", g.Name, g.Short)
			}
			if len(kinds) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  (none registered)")
			}
			return &ui.Error{What: "Cannot select a generator", Why: "a terminal is required for the picker", Try: "spinup g <kind> --help", Code: ui.ExitUsage}
		}
		prompter := p
		if prompter == nil {
			prompter = genrun.HuhPrompter{In: cmd.InOrStdin(), Out: cmd.ErrOrStderr()}
		}
		g, args, err := genrun.Pick(cmd.Context(), kinds, prompter)
		if err != nil {
			return err
		}
		child := children[g.Name]
		child.SetContext(cmd.Context())
		child.InitDefaultHelpFlag()
		if err := child.ParseFlags(args); err != nil {
			return &ui.Error{What: "Invalid generator flags", Why: err.Error(), Try: "spinup g " + g.Name + " --help", Code: ui.ExitUsage, Err: err}
		}
		help, _ := child.Flags().GetBool("help")
		if help {
			return child.Help()
		}
		return child.RunE(child, child.Flags().Args())
	}
	return c
}
