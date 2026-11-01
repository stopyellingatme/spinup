package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/monitor"
)

func init() { Register(GroupOperate, newMonitor) }

func newMonitor(app *App) *cobra.Command {
	group := &cobra.Command{Use: "monitor", Example: `  # Check health, then look at alerts and logs
  spinup monitor status --env production
  spinup monitor alerts --env production
  spinup monitor logs --service api --env production`, Short: "Operate monitoring services through SSH", Args: cobra.NoArgs}
	var accept bool
	group.PersistentFlags().BoolVar(&accept, "accept-new-host-key", false, "accept a previously unknown SSH host key")
	options := func(c *cobra.Command, action string) monitor.Options {
		return monitor.Options{Dir: app.Flags.Dir, Env: app.Flags.Env, Action: action, AcceptNewHostKey: accept, Out: c.OutOrStdout(), ErrOut: c.ErrOrStderr()}
	}
	run := func(c *cobra.Command, o monitor.Options) error { return monitor.Run(c.Context(), o, monitor.Deps{}) }
	var noBrowser bool
	open := &cobra.Command{Use: "open", Example: `  # Open Grafana through an SSH tunnel
  spinup monitor open --env production

  # Print the URL and do not open a browser
  spinup monitor open --no-browser --env production`, Short: "Open Grafana through a local SSH tunnel", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		o := options(c, "open")
		o.NoBrowser = noBrowser
		return run(c, o)
	}}
	open.Flags().BoolVar(&noBrowser, "no-browser", false, "print the URL without opening a browser")
	group.AddCommand(open)
	examples := map[string]string{
		"status":     "  # Show monitoring health and disk use\n  spinup monitor status --env production",
		"alerts":     "  # Show the alerts that fire now\n  spinup monitor alerts --env production",
		"test-alert": "  # Check that alerts reach your contact points\n  spinup monitor test-alert --env production",
	}
	for _, action := range []string{"status", "alerts", "test-alert"} {
		group.AddCommand(&cobra.Command{Use: action, Example: examples[action], Short: map[string]string{"status": "Show monitoring health and disk use", "alerts": "Show firing and pending alerts", "test-alert": "Send a test alert to configured contact points"}[action], Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return run(c, options(c, action)) }})
	}
	var service, since, query string
	logs := &cobra.Command{Use: "logs", Example: `  # Show the last hour of logs for one service
  spinup monitor logs --service api --env production

  # Show the last 15 minutes
  spinup monitor logs --service api --since 15m --env production

  # Use your own LogQL query
  spinup monitor logs --query '{service_name="api"} |= "error"' --env production`, Short: "Query Loki logs", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		o := options(c, "logs")
		o.Service, o.Since, o.Query = service, since, query
		return run(c, o)
	}}
	logs.Flags().StringVar(&service, "service", "", "filter by the service_name label")
	logs.Flags().StringVar(&since, "since", "1h", "query this duration into the past")
	logs.Flags().StringVar(&query, "query", "", "use this LogQL query instead of the service filter")
	group.AddCommand(logs)
	group.AddCommand(&cobra.Command{Use: "trace <trace-id>", Example: `  # Show the span tree of one trace
  spinup monitor trace 4bf92f3577b34da6a3ce929d0e0e4736 --env production`, Short: "Show a Tempo span tree (full profile)", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		o := options(c, "trace")
		o.TraceID = args[0]
		return run(c, o)
	}})
	var duration string
	silence := &cobra.Command{Use: "silence <matcher>", Example: `  # Silence one alert for 2 hours (the default)
  spinup monitor silence alertname=HighErrorRate --env production

  # Silence for 30 minutes
  spinup monitor silence alertname=HighErrorRate --for 30m --env production`, Short: "Silence alerts matching label=value", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		o := options(c, "silence")
		o.Matcher, o.For = args[0], duration
		return run(c, o)
	}}
	silence.Flags().StringVar(&duration, "for", "2h", "silence duration")
	group.AddCommand(silence)
	dashboards := &cobra.Command{Use: "dashboards", Short: "Manage custom Grafana dashboards", Args: cobra.NoArgs}
	dashboards.AddCommand(&cobra.Command{Use: "sync", Example: `  # Export dashboards you edited in the Grafana UI
  spinup monitor dashboards sync --env production`, Short: "Export UI dashboards into the project's custom directory", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return run(c, options(c, "dashboards sync")) }})
	group.AddCommand(dashboards)
	return group
}
