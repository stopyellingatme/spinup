package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	genregistry "github.com/stopyellingatme/spinup/internal/gen/registry"
	genrun "github.com/stopyellingatme/spinup/internal/gen/run"
	pluginrender "github.com/stopyellingatme/spinup/internal/plugin/render"
	pluginrpc "github.com/stopyellingatme/spinup/internal/plugin/rpc"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func init() { Register(GroupDevelop, newGenerate) }

func newGenerate(app *App) *cobra.Command {
	return generateCommand(app, genregistry.All(), nil, app.UI.Interactive)
}

func generateCommand(app *App, kinds []gen.Generator, p genrun.Prompter, interactive func() bool) *cobra.Command {
	var diff, force bool
	var selected *gen.Generator
	c := &cobra.Command{Use: "generate <kind> [args...]", Aliases: []string{"g"}, Short: "Generate application files"}
	c.PersistentFlags().BoolVar(&diff, "diff", false, "show unified diffs without writing")
	c.PersistentFlags().BoolVar(&force, "force", false, "overwrite files with different content")
	run := func(cmd *cobra.Command, g gen.Generator, args []string) error {
		root, err := project.FindRoot(app.Flags.Dir)
		if err != nil {
			return &ui.Error{What: "Cannot find the project", Why: err.Error(), Try: "run inside a spinup project, or pass --dir", Err: err}
		}
		_, err = pluginrender.RunGenerator(cmd.Context(), g, args, cmd.Flags(), genrun.Options{Root: root, DryRun: app.Flags.DryRun, Diff: diff, Force: force, JSON: app.Flags.JSON, Out: cmd.OutOrStdout(), Runner: exec.OSRunner{}})
		return err
	}
	children := make(map[string]*cobra.Command, len(kinds))
	add := func(g gen.Generator) *cobra.Command {
		if child := children[g.Name]; child != nil {
			return child
		}
		child := &cobra.Command{Use: g.Name, Aliases: g.Aliases, Short: g.Short}
		if g.Usage != "" {
			child.Use += " " + g.Usage
		}
		if g.Flags != nil {
			g.Flags(child.Flags())
		}
		child.RunE = func(cmd *cobra.Command, args []string) error { return run(cmd, g, args) }
		children[g.Name] = child
		c.AddCommand(child)
		return child
	}
	for _, g := range kinds {
		add(g)
	}
	available := func(cmd *cobra.Command) ([]gen.Generator, error) {
		return pluginrpc.ProjectGenerators(cmd.Context(), app.Flags.Dir, app.Flags.Offline, kinds, cmd.ErrOrStderr())
	}
	// Project flags are parsed after Cobra chooses a child. Resolve a project-only
	// generator here when Cobra selected the parent instead of a built-in child.
	c.Args = func(cmd *cobra.Command, args []string) error {
		selected = nil
		if len(args) == 0 {
			return nil
		}
		all, err := available(cmd)
		if err != nil {
			return err
		}
		for _, g := range all {
			if g.Name == args[0] {
				selected = &g
				return nil
			}
		}
		return &ui.Error{What: "Cannot generate files", Why: fmt.Sprintf("generator %q is not registered", args[0]), Try: "spinup g <kind> --help", Code: ui.ExitUsage}
	}
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if selected != nil {
			return run(cmd, *selected, args[1:])
		}
		all, err := available(cmd)
		if err != nil {
			return err
		}
		if !interactive() {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Available generators:")
			for _, g := range all {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s\t%s\n", g.Name, g.Short)
			}
			if len(all) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  (none registered)")
			}
			return &ui.Error{What: "Cannot select a generator", Why: "a terminal is required for the picker", Try: "spinup g <kind> --help", Code: ui.ExitUsage}
		}
		prompter := p
		if prompter == nil {
			prompter = genrun.HuhPrompter{In: cmd.InOrStdin(), Out: cmd.ErrOrStderr()}
		}
		g, args, err := genrun.Pick(cmd.Context(), all, prompter)
		if err != nil {
			return err
		}
		child := add(g)
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
	defaultHelp := c.HelpFunc()
	c.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if cmd != c {
			defaultHelp(cmd, args)
			return
		}
		all, err := available(cmd)
		if err != nil {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), err)
			defaultHelp(cmd, args)
			return
		}
		for _, g := range all {
			add(g)
		}
		positional := cmd.Flags().Args()
		if len(positional) > 0 {
			if child := children[positional[0]]; child != nil {
				defaultHelp(child, args)
				return
			}
		}
		defaultHelp(cmd, args)
	})
	return c
}
