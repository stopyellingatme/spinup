package cli

import (
	"os"

	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/db"
	exec "github.com/stopyellingatme/spinup/internal/exec"
)

func newDBMigrate(app *App) *cobra.Command {
	var know bool
	c := &cobra.Command{Use: "migrate [up|down|status]", Short: "Run migrations through the application API", Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		sub := "up"
		if len(args) > 0 {
			sub = args[0]
		}
		return db.Migrate(c.Context(), db.Options{Dir: app.Flags.Dir, Sub: sub, Env: app.Flags.Env, IKnow: know, Interactive: app.UI.Interactive(), Yes: app.Flags.Yes, CI: os.Getenv("CI") != "", ConfirmName: app.Flags.ConfirmEnv, DryRun: app.Flags.DryRun, In: c.InOrStdin(), Out: c.OutOrStdout(), ErrOut: c.ErrOrStderr()}, exec.OSRunner{})
	}}
	c.Flags().BoolVar(&know, "i-know", false, "acknowledge that down can lose data")
	return c
}
