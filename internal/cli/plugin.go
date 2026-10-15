package cli

import (
	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/plugin/pack"
	"github.com/stopyellingatme/spinup/internal/project"
)

func init() {
	pack.SetVersionProvider(func() string { return Version })
	Register(GroupMaintain, newPlugin)
}

// pluginSubcommands holds the builders of the plugin subcommands that register with registerPlugin.
var pluginSubcommands []func(app *App) *cobra.Command

// registerPlugin adds a subcommand to `spinup plugin`. Call it from init().
func registerPlugin(build func(app *App) *cobra.Command) {
	regMu.Lock()
	defer regMu.Unlock()
	pluginSubcommands = append(pluginSubcommands, build)
}

// The M10 units register their plugin subcommands; this keeps the pre-seed lint-clean.
var _ = registerPlugin

func newPlugin(app *App) *cobra.Command {
	command := &cobra.Command{Use: "plugin", Short: "Manage trusted template pack plugins"}
	manager := func(c *cobra.Command, trust bool) (*pack.Manager, error) {
		root, err := project.FindRoot(app.Flags.Dir)
		if err != nil {
			return nil, err
		}
		return &pack.Manager{Root: root, CLIVersion: Version, Offline: app.Flags.Offline, Trust: trust, Interactive: app.UI.Interactive(), DryRun: app.Flags.DryRun, In: c.InOrStdin(), Out: c.OutOrStdout()}, nil
	}
	var addVersion, updateVersion string
	var addTrust, updateTrust bool
	add := &cobra.Command{Use: "add <source>", Short: "Inspect and trust a plugin without rendering files", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		m, err := manager(c, addTrust)
		if err != nil {
			return err
		}
		return m.Add(c.Context(), args[0], addVersion)
	}}
	add.Flags().StringVar(&addVersion, "version", "", "fixed git tag or path plugin version")
	add.Flags().BoolVar(&addTrust, "trust", false, "trust the displayed plugin source and hash")
	update := &cobra.Command{Use: "update <name>", Short: "Inspect and trust a plugin version", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		m, err := manager(c, updateTrust)
		if err != nil {
			return err
		}
		return m.Update(c.Context(), args[0], updateVersion)
	}}
	update.Flags().StringVar(&updateVersion, "version", "", "new fixed git tag or path plugin version")
	update.Flags().BoolVar(&updateTrust, "trust", false, "trust the displayed plugin source and hash")
	remove := &cobra.Command{Use: "remove <name>", Short: "Remove plugin records and retain rendered files", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		m, err := manager(c, false)
		if err != nil {
			return err
		}
		return m.Remove(c.Context(), args[0])
	}}
	list := &cobra.Command{Use: "list", Short: "List declared plugins and their locked hashes", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		m, err := manager(c, false)
		if err != nil {
			return err
		}
		entries, err := m.List()
		if err != nil {
			return err
		}
		if app.Flags.JSON {
			return app.UI.JSON(entries)
		}
		for _, entry := range entries {
			app.UI.Printf("%s\t%s\t%s\t%s\n", entry.Name, entry.Version, entry.Source, entry.SHA256)
		}
		return nil
	}}
	verify := &cobra.Command{Use: "verify", Short: "Re-fetch plugins and verify their trust records", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		m, err := manager(c, false)
		if err != nil {
			return err
		}
		return m.Verify(c.Context())
	}}
	info := &cobra.Command{Use: "info <name>", Short: "Show verified plugin metadata, generators, and files", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		m, err := manager(c, false)
		if err != nil {
			return err
		}
		return m.Info(c.Context(), args[0])
	}}
	command.AddCommand(add, remove, list, verify, info, update)
	for _, build := range pluginSubcommands {
		command.AddCommand(build(app))
	}
	return command
}
