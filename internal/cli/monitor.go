package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/monitor"
)

func init() { Register(GroupOperate, newMonitor) }

func newMonitor(app *App) *cobra.Command {
	group := &cobra.Command{Use: "monitor", Short: "Operate monitoring services through SSH", Args: cobra.NoArgs}
	var accept bool
	group.PersistentFlags().BoolVar(&accept, "accept-new-host-key", false, "accept a previously unknown SSH host key")
	options := func(c *cobra.Command, action string) monitor.Options {
		return monitor.Options{Dir: app.Flags.Dir, Env: app.Flags.Env, Action: action, AcceptNewHostKey: accept, Out: c.OutOrStdout(), ErrOut: c.ErrOrStderr()}
	}
	run := func(c *cobra.Command, o monitor.Options) error { return monitor.Run(c.Context(), o, monitor.Deps{}) }
	var noBrowser bool
	open := &cobra.Command{Use: "open", Short: "Open Grafana through a local SSH tunnel", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		o := options(c, "open")
		o.NoBrowser = noBrowser
		return run(c, o)
	}}
	open.Flags().BoolVar(&noBrowser, "no-browser", false, "print the URL without opening a browser")
	group.AddCommand(open)
	for _, action := range []string{"status", "alerts", "test-alert"} {
		group.AddCommand(&cobra.Command{Use: action, Short: map[string]string{"status": "Show monitoring health and disk use", "alerts": "Show firing and pending alerts", "test-alert": "Send a test alert to configured contact points"}[action], Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return run(c, options(c, action)) }})
	}
	var service, since, query string
	logs := &cobra.Command{Use: "logs", Short: "Query Loki logs", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		o := options(c, "logs")
		o.Service, o.Since, o.Query = service, since, query
		return run(c, o)
	}}
	logs.Flags().StringVar(&service, "service", "", "filter by the service_name label")
	logs.Flags().StringVar(&since, "since", "1h", "query this duration into the past")
	logs.Flags().StringVar(&query, "query", "", "use this LogQL query instead of the service filter")
	group.AddCommand(logs)
	group.AddCommand(&cobra.Command{Use: "trace <trace-id>", Short: "Show a Tempo span tree (full profile)", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		o := options(c, "trace")
		o.TraceID = args[0]
		return run(c, o)
	}})
	var duration string
	silence := &cobra.Command{Use: "silence <matcher>", Short: "Silence alerts matching label=value", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		o := options(c, "silence")
		o.Matcher, o.For = args[0], duration
		return run(c, o)
	}}
	silence.Flags().StringVar(&duration, "for", "2h", "silence duration")
	group.AddCommand(silence)
	dashboards := &cobra.Command{Use: "dashboards", Short: "Manage custom Grafana dashboards", Args: cobra.NoArgs}
	dashboards.AddCommand(&cobra.Command{Use: "sync", Short: "Export UI dashboards into the project's custom directory", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return run(c, options(c, "dashboards sync")) }})
	group.AddCommand(dashboards)
	return group
}
