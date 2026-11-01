package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/deploy"
)

func init() { Register(GroupShip, newReleases) }

func newReleases(app *App) *cobra.Command {
	run := func(action string, id string, keep int, c *cobra.Command) error {
		return deploy.Releases(c.Context(), deploy.ReleasesOptions{Options: deployOptions(app, c), Action: action, ID: id, Keep: keep}, deploy.DefaultDeps())
	}
	c := &cobra.Command{Use: "releases", Example: `  # List the stored releases
  spinup releases --env production`, Short: "Inspect and prune stored releases", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return run("list", "", 0, c) }}
	list := &cobra.Command{Use: "list", Example: `  # List the release history and the current release
  spinup releases list --env production`, Short: "List the release history and current marker", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return run("list", "", 0, c) }}
	show := &cobra.Command{Use: "show <id>", Example: `  # Show one release (secrets appear as hashes)
  spinup releases show 20261101T093000Z-ab12cd3 --env production`, Short: "Show a release snapshot with secret hashes only", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error { return run("show", args[0], 0, c) }}
	var keep int
	prune := &cobra.Command{Use: "prune", Example: `  # Keep only the 5 newest releases
  spinup releases prune --keep 5 --env production`, Short: "Prune old releases while retaining current and previous", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return run("prune", "", keep, c) }}
	prune.Flags().IntVar(&keep, "keep", deploy.KeepReleases, "number of releases to retain (always retains current and previous)")
	c.AddCommand(list, show, prune)
	return c
}
