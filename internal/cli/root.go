package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"sort"
	"sync"

	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/ui"
)

// Group is a command group shown in the help output.
type Group string

// Command groups, in help order.
const (
	GroupDevelop  Group = "develop"
	GroupData     Group = "data"
	GroupShip     Group = "ship"
	GroupOperate  Group = "operate"
	GroupMaintain Group = "maintain"
)

var groupOrder = []Group{GroupDevelop, GroupData, GroupShip, GroupOperate, GroupMaintain}

var groupTitles = map[Group]string{
	GroupDevelop:  "Develop",
	GroupData:     "Data",
	GroupShip:     "Ship",
	GroupOperate:  "Operate",
	GroupMaintain: "Maintain",
}

// GlobalFlags holds the persistent flags of the root command.
type GlobalFlags struct {
	Dir, Env       string
	ConfirmEnv     string
	Yes            bool
	NonInteractive bool
	JSON           bool
	DryRun         bool
	Verbose        bool
	NoColor        bool
	Offline        bool
}

// App is the shared state passed to every command builder.
type App struct {
	UI    *ui.Printer
	Flags *GlobalFlags
}

type registration struct {
	group Group
	build func(app *App) *cobra.Command
}

var (
	regMu    sync.Mutex
	registry []registration
)

// Register adds a command to the root command. Call it from init().
func Register(group Group, build func(app *App) *cobra.Command) {
	regMu.Lock()
	defer regMu.Unlock()
	registry = append(registry, registration{group: group, build: build})
}

const gettingStarted = `spinup creates and operates a full-stack app.

Get started:
  spinup new <name>    Create a new app
  spinup dev           Run the app in development
  spinup --help        List all commands
`

func (f *GlobalFlags) options() ui.Options {
	return ui.Options{JSON: f.JSON, NoColor: f.NoColor, NonInteractive: f.NonInteractive, Verbose: f.Verbose}
}

func newRoot(app *App, in io.Reader, out, errOut io.Writer, entered *bool) *cobra.Command {
	root := &cobra.Command{
		Use:           "spinup",
		Short:         "Create and operate a full-stack app",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// Cobra validates arguments before this hook, so a later error is not a usage error.
			*entered = true
			*app.UI = *ui.New(in, out, errOut, app.Flags.options())
			printUpdateNotice(cmd, app, errOut)
			return nil
		},
		RunE: func(*cobra.Command, []string) error {
			app.UI.Printf("%s", gettingStarted)
			return nil
		},
	}
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(errOut)

	f := app.Flags
	pf := root.PersistentFlags()
	pf.StringVarP(&f.Dir, "dir", "C", "", "run in this project directory")
	pf.StringVar(&f.Env, "env", "", "target environment")
	pf.BoolVarP(&f.Yes, "yes", "y", false, "answer yes to prompts")
	pf.StringVar(&f.ConfirmEnv, "confirm-env", "", "type the protected environment name without a terminal")
	pf.BoolVar(&f.NonInteractive, "non-interactive", false, "never prompt")
	pf.BoolVar(&f.JSON, "json", false, "write JSON output")
	pf.BoolVar(&f.DryRun, "dry-run", false, "show changes without writing")
	pf.BoolVar(&f.Verbose, "verbose", false, "write extra detail")
	pf.BoolVar(&f.NoColor, "no-color", false, "turn off color")
	pf.BoolVar(&f.Offline, "offline", os.Getenv("SPINUP_OFFLINE") == "1", "never download template versions")

	regMu.Lock()
	regs := make([]registration, len(registry))
	copy(regs, registry)
	regMu.Unlock()
	cmds := make([]*cobra.Command, 0, len(regs))
	used := map[Group]bool{}
	for _, r := range regs {
		c := r.build(app)
		c.GroupID = string(r.group)
		cmds = append(cmds, c)
		used[r.group] = true
	}
	// Add only the groups that have commands, so help shows no empty headings.
	for _, g := range groupOrder {
		if used[g] {
			root.AddGroup(&cobra.Group{ID: string(g), Title: groupTitles[g]})
		}
	}
	sort.SliceStable(cmds, func(i, j int) bool { return cmds[i].Name() < cmds[j].Name() })
	root.AddCommand(cmds...)
	return root
}

// Execute runs spinup with args and returns the process exit code.
func Execute(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	app := &App{Flags: &GlobalFlags{}}
	app.UI = ui.New(in, out, errOut, ui.Options{})
	entered := false
	root := newRoot(app, in, out, errOut, &entered)
	root.SetArgs(args)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &ui.Error{What: "Invalid flag", Why: err.Error(), Code: ui.ExitUsage}
	})

	cmd, err := root.ExecuteContextC(ctx)
	if err == nil {
		return ui.ExitOK
	}
	var ue *ui.Error
	if !errors.As(err, &ue) && !entered {
		err = &ui.Error{What: "Invalid usage", Why: err.Error(), Code: ui.ExitUsage}
	}
	if errors.As(err, &ue) && ue.Code == ui.ExitUsage && ue.Try == "" {
		cp := *ue
		if cmd == nil {
			cmd = root
		}
		cp.Try = cmd.CommandPath() + " --help"
		err = &cp
	}
	// Cobra may fail before it parses flags, so read the output flags from args too.
	opts := app.Flags.options()
	for _, a := range args {
		if a == "--" {
			break
		}
		switch a {
		case "--json", "--json=true":
			opts.JSON = true
		case "--no-color", "--no-color=true":
			opts.NoColor = true
		}
	}
	ui.New(in, out, errOut, opts).RenderError(err)
	return ui.ExitCode(err)
}
