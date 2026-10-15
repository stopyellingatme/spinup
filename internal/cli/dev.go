package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/devloop"
	"github.com/stopyellingatme/spinup/internal/exec"
)

var notifyContext func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) = signal.NotifyContext

func init() { Register(GroupDevelop, newDev) }
func newDev(app *App) *cobra.Command {
	var options devloop.Options
	command := &cobra.Command{Use: "dev", Short: "Run the local development stack", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		options.Dir = app.Flags.Dir
		options.NoColor = app.Flags.NoColor
		options.Out = c.OutOrStdout()
		ctx, stop := devContext(c.Context())
		defer stop()
		return devloop.Run(ctx, options, exec.OSRunner{})
	}}
	flags := command.Flags()
	flags.BoolVar(&options.NoWeb, "no-web", false, "skip the Vite dev server")
	flags.BoolVar(&options.NoInfra, "no-infra", false, "skip Docker infrastructure")
	flags.BoolVar(&options.LGTM, "lgtm", false, "start Grafana LGTM monitoring")
	flags.IntVar(&options.Port, "port", 5173, "preferred web port")
	flags.IntVar(&options.APIPort, "api-port", 8080, "preferred API port")
	flags.StringVar(&options.Watch, "watch", "air", "Go watcher: air or wgo")
	flags.BoolVar(&options.Down, "down", false, "stop Docker infrastructure on exit")
	return command
}

func devContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return notifyContext(ctx, syscall.SIGHUP)
}
