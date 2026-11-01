package cli

import (
	"github.com/spf13/cobra"
	"github.com/stopyellingatme/spinup/internal/maintain"
)

// maintenanceSubcommands holds builders added by maintenance feature units.
var maintenanceSubcommands []func(app *App) *cobra.Command

// registerMaintenance adds a subcommand to `spinup maintenance`.
func registerMaintenance(build func(app *App) *cobra.Command) {
	regMu.Lock()
	defer regMu.Unlock()
	maintenanceSubcommands = append(maintenanceSubcommands, build)
}

func init() {
	registerMaintenance(newMaintenancePatch)
	registerMaintenance(newMaintenanceVulnScan)
	registerMaintenance(newMaintenancePrune)
	registerMaintenance(newMaintenanceVacuum)
	registerMaintenance(newMaintenanceCerts)
	registerMaintenance(newMaintenanceWindow)
	Register(GroupMaintain, newMaintenance)
}

func newMaintenance(app *App) *cobra.Command {
	group := &cobra.Command{Use: "maintenance", Example: `  # Check the window, then list pending package updates
  spinup maintenance window --env production
  spinup maintenance patch --plan --env production`, Short: "Keep a deployed VM healthy", Args: cobra.NoArgs}
	for _, build := range maintenanceSubcommands {
		group.AddCommand(build(app))
	}
	return group
}

func maintenanceOptions(app *App, c *cobra.Command, action string) maintain.Options {
	return maintain.Options{Dir: app.Flags.Dir, Env: app.Flags.Env, Action: action, JSON: app.Flags.JSON, Out: c.OutOrStdout(), ErrOut: c.ErrOrStderr()}
}

func runMaintenance(c *cobra.Command, o maintain.Options) error {
	return maintain.Run(c.Context(), o, maintain.Deps{})
}

func newMaintenancePatch(app *App) *cobra.Command {
	var plan, reboot, now bool
	patch := &cobra.Command{Use: "patch", Example: `  # List the package updates without installing them
  spinup maintenance patch --plan --env production

  # Install updates and reboot if the host needs it
  spinup maintenance patch --reboot --env production`, Short: "Install available package updates", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		o := maintenanceOptions(app, c, "patch")
		o.Plan, o.Reboot, o.Now = plan || app.Flags.DryRun, reboot, now
		return runMaintenance(c, o)
	}}
	patch.Flags().BoolVar(&plan, "plan", false, "list packages without installing them")
	patch.Flags().BoolVar(&reboot, "reboot", false, "reboot when the host requires it")
	patch.Flags().BoolVar(&now, "now", false, "ignore the maintenance window")
	return patch
}

func newMaintenanceVulnScan(app *App) *cobra.Command {
	var failOn string
	vuln := &cobra.Command{Use: "vuln-scan", Example: `  # Scan the running images and fail on critical findings only
  spinup maintenance vuln-scan --fail-on critical --env production

  # Scan with the default level (high)
  spinup maintenance vuln-scan --env production`, Short: "Scan running images with Trivy", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		o := maintenanceOptions(app, c, "vuln-scan")
		o.FailOn = failOn
		return runMaintenance(c, o)
	}}
	vuln.Flags().StringVar(&failOn, "fail-on", "high", "fail on high or critical, or critical")
	return vuln
}

func newMaintenancePrune(app *App) *cobra.Command {
	var prunePlan bool
	prune := &cobra.Command{Use: "prune", Example: `  # List the images that would be removed
  spinup maintenance prune --plan --env production

  # Remove unused images and build cache
  spinup maintenance prune --env production`, Short: "Remove unused app images and build cache", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		o := maintenanceOptions(app, c, "prune")
		o.Plan = prunePlan || app.Flags.DryRun
		return runMaintenance(c, o)
	}}
	prune.Flags().BoolVar(&prunePlan, "plan", false, "list images without removing them")
	return prune
}

func newMaintenanceVacuum(app *App) *cobra.Command {
	var full string
	var now bool
	vacuum := &cobra.Command{Use: "vacuum", Example: `  # Vacuum the production database
  spinup maintenance vacuum --env production

  # Run VACUUM FULL on one table, outside the window
  spinup maintenance vacuum --full tickets --now --env production`, Short: "Vacuum the production database", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		o := maintenanceOptions(app, c, "vacuum")
		o.FullTable, o.Now = full, now
		return runMaintenance(c, o)
	}}
	vacuum.Flags().StringVar(&full, "full", "", "run VACUUM FULL on one table")
	vacuum.Flags().BoolVar(&now, "now", false, "ignore the maintenance window")
	return vacuum
}

func newMaintenanceCerts(app *App) *cobra.Command {
	return &cobra.Command{Use: "certs", Example: `  # Show when the public certificate expires
  spinup maintenance certs --env production`, Short: "Check the public certificate expiry", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return runMaintenance(c, maintenanceOptions(app, c, "certs"))
	}}
}

func newMaintenanceWindow(app *App) *cobra.Command {
	return &cobra.Command{Use: "window", Example: `  # Show the maintenance window
  spinup maintenance window --env production`, Short: "Show the maintenance window", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return runMaintenance(c, maintenanceOptions(app, c, "window"))
	}}
}
