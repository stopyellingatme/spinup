package cli

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/stopyellingatme/spinup/internal/backup"
	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func init() { Register(GroupShip, newDeploy) }

func deployOptions(app *App, c *cobra.Command) deploy.Options {
	return deploy.Options{
		Dir: app.Flags.Dir, Env: app.Flags.Env, JSON: app.Flags.JSON,
		Out: c.OutOrStdout(), ErrOut: c.ErrOrStderr(),
		Confirm: confirmFor(app, c),
	}
}

// projectRoot returns the project root of dir, or dir when it is not in a project.
// The deploy preflight reports a missing project.
func projectRoot(dir string) string {
	if dir == "" {
		dir = "."
	}
	if root, err := project.FindRoot(dir); err == nil {
		return root
	}
	return dir
}

// addDeployFlags defines the deploy flags that both spinup deploy and
// spinup deploy digital-ocean accept.
func addDeployFlags(f *pflag.FlagSet, o *deploy.Options) {
	f.BoolVar(&o.AllowDirty, "allow-dirty", false, "deploy with uncommitted changes")
	f.BoolVar(&o.AllowDestructive, "allow-destructive", false, "deploy migrations that db lint reports as unsafe")
	f.BoolVar(&o.SkipBuild, "skip-build", false, "use the images of the release that exist already")
	f.BoolVar(&o.NoAutoRollback, "no-auto-rollback", false, "leave an unhealthy new color running for debugging")
	f.StringVar(&o.Tag, "tag", "", "release ID of images that exist already (<YYYYMMDDTHHMMSSZ>-<12-char SHA>)")
	f.StringVar(&o.Platform, "platform", "", "image platform (default linux/amd64)")
	f.DurationVar(&o.HealthTimeout, "health-timeout", deploy.DefaultHealthTimeout, "time for the new color to become healthy")
}

func newDeploy(app *App) *cobra.Command {
	var o deploy.Options
	var acceptNewHostKey bool
	c := &cobra.Command{
		Use:   "deploy",
		Short: "Ship the current commit to an environment with zero downtime",
		Long: "Build the images, ship them to the VM of the environment over SSH, run the migrations,\n" +
			"and switch Caddy to the new blue/green color. Each step is journaled on the VM.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if o.Resume && (o.Plan || app.Flags.DryRun) {
				return &ui.Error{What: "Invalid flags", Why: "--plan and --resume cannot be used together", Code: ui.ExitUsage, Try: "run spinup deploy --plan, or spinup deploy --resume"}
			}
			opts := deployOptions(app, c)
			opts.Plan = o.Plan || app.Flags.DryRun
			opts.Redeploy = o.Redeploy
			opts.Resume, opts.AllowDirty, opts.AllowDestructive = o.Resume, o.AllowDirty, o.AllowDestructive
			opts.SkipBuild, opts.SkipBackup, opts.NoAutoRollback = o.SkipBuild, o.SkipBackup, o.NoAutoRollback
			opts.Tag, opts.Platform, opts.HealthTimeout, opts.AcceptNewHostKey = o.Tag, o.Platform, o.HealthTimeout, acceptNewHostKey
			// The backup hook reads its flags from the context (HookContext has no field for them).
			ctx := backup.WithDeployOptions(c.Context(), backup.DeployOptions{Dir: projectRoot(app.Flags.Dir), SkipBackup: o.SkipBackup})
			return deploy.Deploy(ctx, opts, deploy.DefaultDeps())
		},
	}
	f := c.Flags()
	f.BoolVar(&o.Plan, "plan", false, "print what would change and change nothing on the VM")
	f.BoolVar(&o.Resume, "resume", false, "continue the last deploy from its first step that is not done")
	f.BoolVar(&o.Redeploy, "redeploy", false, "reuse a release ID in a new directory with a -r<N> suffix")
	f.BoolVar(&o.SkipBackup, "skip-backup", false, "skip the pre-deploy backup")
	addDeployFlags(f, &o)
	c.PersistentFlags().BoolVar(&acceptNewHostKey, "accept-new-host-key", false, "accept the SSH host key of a new VM")

	status := &cobra.Command{
		Use:   "status",
		Short: "Show the current release, the deploy lock, and the last deploy journal",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			opts := deployOptions(app, c)
			opts.AcceptNewHostKey = acceptNewHostKey
			return deploy.Status(c.Context(), opts, deploy.DefaultDeps())
		},
	}
	var force bool
	unlock := &cobra.Command{
		Use:   "unlock",
		Short: "Remove the deploy lock of an environment",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			opts := deployOptions(app, c)
			opts.AcceptNewHostKey = acceptNewHostKey
			return deploy.Unlock(c.Context(), opts, deploy.DefaultDeps(), force)
		},
	}
	unlock.Flags().BoolVar(&force, "force", false, "remove the lock without an ownership check")
	c.AddCommand(status, unlock, newDeployDigitalOcean(app))
	return c
}
