package cli

import (
	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/tools"
)

func init() { Register(GroupMaintain, newTools) }

func newTools(app *App) *cobra.Command {
	command := &cobra.Command{Use: "tools", Short: "Install and inspect checksum-verified host tools"}
	var force bool
	install := &cobra.Command{
		Use: "install [sops|age...]", Short: "Download and verify pinned tool releases", Args: cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, names []string) error {
			installed, err := tools.Install(c.Context(), tools.Options{Names: names, Offline: app.Flags.Offline, Force: force})
			if err != nil {
				return err
			}
			if app.Flags.JSON {
				return app.UI.JSON(installed)
			}
			for _, item := range installed {
				status := "installed"
				if item.Skipped {
					status = "current"
				}
				app.UI.Printf("%s\t%s\t%s\t%s\n", item.Name, item.Version, status, item.Path)
			}
			return nil
		},
	}
	install.Flags().BoolVar(&force, "force", false, "reinstall tools that already report the pinned version")
	list := &cobra.Command{
		Use: "list", Short: "List pinned tools and installed versions", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			entries, err := tools.List(c.Context(), tools.Options{})
			if err != nil {
				return err
			}
			if app.Flags.JSON {
				return app.UI.JSON(entries)
			}
			for _, entry := range entries {
				first := entry.PathFirst
				if first == "" {
					first = "none"
				}
				app.UI.Printf("%s\tpinned %s\tinstalled %s\t%s\tPATH %s\n", entry.Name, entry.Version, entry.Installed, entry.Path, first)
			}
			return nil
		},
	}
	command.AddCommand(install, list)
	return command
}
