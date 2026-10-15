package cli

import (
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/templates/store"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func init() { Register(GroupMaintain, newCache) }

func newCache(app *App) *cobra.Command {
	command := &cobra.Command{Use: "cache", Short: "Manage cached template versions"}
	warm := &cobra.Command{
		Use: "warm [version...]", Short: "Download and verify missing template versions",
		Args: func(_ *cobra.Command, _ []string) error {
			if app.Flags.Offline || os.Getenv("SPINUP_OFFLINE") == "1" {
				return &ui.Error{What: "Cannot warm templates", Why: "offline mode disables downloads", Try: "remove --offline and unset SPINUP_OFFLINE", Code: ui.ExitUsage}
			}
			return nil
		},
		RunE: func(c *cobra.Command, args []string) error {
			source, err := store.Default(Version, app.Flags.Offline)
			if err != nil {
				return err
			}
			if app.Flags.DryRun {
				app.UI.Printf("Would warm templates: %s\n", strings.Join(args, ", "))
				return nil
			}
			if len(args) == 0 {
				version, err := source.WarmProject(c.Context(), app.Flags.Dir)
				if err != nil {
					return err
				}
				args = []string{version}
			} else if err := source.Warm(c.Context(), args); err != nil {
				return err
			}
			app.UI.Printf("Warmed templates: %s\n", strings.Join(args, ", "))
			return nil
		},
	}
	list := &cobra.Command{Use: "list", Short: "List loadable template versions and their sources", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		source, err := store.Default(Version, app.Flags.Offline)
		if err != nil {
			return err
		}
		entries, err := source.List(c.Context())
		if err != nil {
			return err
		}
		if app.Flags.JSON {
			return app.UI.JSON(entries)
		}
		for _, entry := range entries {
			app.UI.Printf("%s\t%s\n", entry.Version, entry.Source)
		}
		return nil
	}}
	clean := &cobra.Command{Use: "clean", Short: "Remove downloaded template archives and signatures", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		source, err := store.Default(Version, app.Flags.Offline)
		if err != nil {
			return err
		}
		if app.Flags.DryRun {
			app.UI.Printf("Would remove %s\n", source.CacheDir())
			return nil
		}
		if err := source.Clean(); err != nil {
			return err
		}
		app.UI.Printf("Removed %s\n", source.CacheDir())
		return nil
	}}
	command.AddCommand(warm, list, clean)
	return command
}
