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
	c := &cobra.Command{Use: "releases", Short: "Inspect and prune stored releases", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return run("list", "", 0, c) }}
	list := &cobra.Command{Use: "list", Short: "List the release history and current marker", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return run("list", "", 0, c) }}
	show := &cobra.Command{Use: "show <id>", Short: "Show a release snapshot with secret hashes only", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error { return run("show", args[0], 0, c) }}
	var keep int
	prune := &cobra.Command{Use: "prune", Short: "Prune old releases while retaining current and previous", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return run("prune", "", keep, c) }}
	prune.Flags().IntVar(&keep, "keep", deploy.KeepReleases, "number of releases to retain (always retains current and previous)")
	c.AddCommand(list, show, prune)
	return c
}
