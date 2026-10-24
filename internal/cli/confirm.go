package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/project"
)

// confirmFor builds the protected-environment confirmation from the global flags.
// Every command that can target a protected environment uses this helper.
func confirmFor(app *App, c *cobra.Command) project.Confirm {
	return project.Confirm{
		In: c.InOrStdin(), Out: c.ErrOrStderr(),
		Interactive: app.UI.Interactive(),
		Yes:         app.Flags.Yes, CI: os.Getenv("CI") != "",
		Name: app.Flags.ConfirmEnv,
	}
}
