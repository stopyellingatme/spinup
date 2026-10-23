package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/backup"
	"github.com/stopyellingatme/spinup/internal/browser"
	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean"
	"github.com/stopyellingatme/spinup/internal/cloudauth"
	"github.com/stopyellingatme/spinup/internal/deploy"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/provision"
	"github.com/stopyellingatme/spinup/internal/ui"
)

const (
	doTokenFile = "digitalocean.json"
	doTryToken  = "export DIGITALOCEAN_ACCESS_TOKEN, or run spinup deploy digital-ocean login in a terminal"
)

// doSession is the token source and the API client of one command run.
type doSession struct {
	source *cloudauth.Source
	client *digitalocean.Client
}

// newDOSession refuses --offline and builds the token source and the API client.
// noBrowser makes the login print the URL and skip the browser opener.
func newDOSession(app *App, c *cobra.Command, noBrowser bool) (*doSession, error) {
	if app.Flags.Offline {
		return nil, &ui.Error{What: "Use DigitalOcean", Why: "the command needs the network, and --offline is set", Code: ui.ExitUsage, Try: "run the command without --offline"}
	}
	oauth, err := digitalocean.OAuthBase()
	if err != nil {
		return nil, &ui.Error{What: "Read the DigitalOcean settings", Why: err.Error(), Code: ui.ExitUsage}
	}
	api, err := digitalocean.APIBase()
	if err != nil {
		return nil, &ui.Error{What: "Read the DigitalOcean settings", Why: err.Error(), Code: ui.ExitUsage}
	}
	dir, err := cloudauth.DefaultDir()
	if err != nil {
		return nil, err
	}
	cfg := cloudauth.Config{
		AuthorizeURL: oauth + digitalocean.AuthorizePath,
		TokenURL:     oauth + digitalocean.TokenPath,
		RevokeURL:    oauth + digitalocean.RevokePath,
		ClientID:     digitalocean.ClientID(),
		Scopes:       digitalocean.Scopes,
	}
	errOut := c.ErrOrStderr()
	src := &cloudauth.Source{
		Getenv:      os.Getenv,
		EnvNames:    []string{"DIGITALOCEAN_ACCESS_TOKEN", "DIGITALOCEAN_TOKEN"},
		Warn:        errOut,
		Store:       cloudauth.Store{Dir: dir, Name: doTokenFile},
		Config:      cfg,
		Interactive: app.UI.Interactive(),
		TryHint:     doTryToken,
	}
	src.Login = func(ctx context.Context) (cloudauth.Token, error) {
		return doLogin(ctx, cfg, errOut, noBrowser)
	}
	return &doSession{source: src, client: digitalocean.New(api, src.Token)}, nil
}

// doLogin runs one browser login. It fails when the build has no client ID.
func doLogin(ctx context.Context, cfg cloudauth.Config, errOut io.Writer, noBrowser bool) (cloudauth.Token, error) {
	if cfg.ClientID == "" {
		return cloudauth.Token{}, &ui.Error{
			What: "Sign in to DigitalOcean",
			Why:  "the browser sign-in is not available in this build",
			Code: ui.ExitError,
			Try:  "export DIGITALOCEAN_ACCESS_TOKEN=<a personal access token> (create one at https://cloud.digitalocean.com/account/api/tokens), or set SPINUP_DO_CLIENT_ID",
		}
	}
	o := cloudauth.LoginOptions{Out: errOut, TryHint: "run the command again with --no-browser, or " + doTryToken}
	if !noBrowser {
		o.Open = func(u string) error { return browser.Open(ctx, runexec.OSRunner{}, u) }
	}
	return cloudauth.Login(ctx, cfg, o)
}

// doInfo is the info object that DigitalOcean returns with a token.
type doInfo struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	TeamName string `json:"team_name"`
}

func newDeployDigitalOcean(app *App) *cobra.Command {
	var o provision.Options
	var dopts deploy.Options
	var noBrowser bool
	c := &cobra.Command{
		Use:     "digital-ocean",
		Aliases: []string{"digitalocean", "do"},
		Short:   "Create a DigitalOcean droplet for the app and deploy to it",
		Long: "Sign in to DigitalOcean, create a droplet with a firewall and a DNS record, make the secrets,\n" +
			"commit the new files, and run the normal deploy. A second run continues after a failure.\n" +
			"After the first run, plain spinup deploy needs no DigitalOcean token.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := refuseDryRun(app, "login", "run the command without --dry-run"); err != nil {
				return err
			}
			sess, err := newDOSession(app, c, noBrowser)
			if err != nil {
				return err
			}
			opts := doOptions(app, c, o)
			opts.Plan = o.Plan || app.Flags.DryRun
			opts.Deploy = passDeployFlags(deployOptions(app, c), dopts)
			root := projectRoot(app.Flags.Dir)
			deployFn := func(ctx context.Context, d deploy.Options) error {
				ctx = backup.WithDeployOptions(ctx, backup.DeployOptions{Dir: root, SkipBackup: d.SkipBackup})
				return deploy.Deploy(ctx, d, deploy.DefaultDeps())
			}
			return provision.Up(c.Context(), opts, provision.DefaultDeps(sess.client, deployFn))
		},
	}
	f := c.Flags()
	f.StringVar(&o.Region, "region", "", "DigitalOcean region slug (default from the stored value, then nyc3)")
	f.StringVar(&o.Size, "size", "", "droplet size slug (default s-2vcpu-4gb for production, else s-1vcpu-2gb)")
	f.StringVar(&o.Image, "image", "", "droplet image slug (Ubuntu only)")
	f.StringVar(&o.Domain, "domain", "", "host name of the app (DigitalOcean DNS, or an external record that you create)")
	f.StringVar(&o.PublicURL, "public-url", "", "public URL of the app (default from the domain, else an sslip.io address)")
	f.StringVar(&o.SSHKey, "ssh-key", "", "operator SSH public key file")
	f.StringVar(&o.SSHFrom, "ssh-from", "", "comma list of sources that may reach port 22 (default all)")
	f.BoolVar(&noBrowser, "no-browser", false, "print the sign-in URL and do not open a browser")
	f.StringVar(&o.RecoveryRecipient, "recovery-recipient", "", "age recipient that can recover the secrets")
	f.BoolVar(&o.NoRecovery, "no-recovery", false, "make the secrets with no recovery recipient")
	f.BoolVar(&o.Plan, "plan", false, "print what would change and change nothing")
	f.BoolVar(&o.SkipDeploy, "skip-deploy", false, "stop before the deploy")
	f.BoolVar(&o.NoCommit, "no-commit", false, "do not commit the provisioning files")
	f.BoolVar(&o.Recreate, "recreate", false, "delete the droplet and create a new one")
	f.BoolVar(&o.IKnow, "i-know", false, "accept the loss of data when --recreate deletes a droplet with a release")
	f.DurationVar(&o.DNSTimeout, "dns-timeout", 10*time.Minute, "time to wait for a DNS record that DigitalOcean does not hold")
	addDeployFlags(f, &dopts)

	c.AddCommand(newDOLogin(app), newDOLogout(app), newDOStatus(app), newDODestroy(app))
	return c
}

// doOptions copies the shared fields of the provision options from the globals.
func doOptions(app *App, c *cobra.Command, o provision.Options) provision.Options {
	d := deployOptions(app, c)
	o.Dir, o.Env, o.JSON = d.Dir, d.Env, d.JSON
	o.Confirm = d.Confirm
	o.Out, o.ErrOut = d.Out, d.ErrOut
	return o
}

// passDeployFlags copies the pass-through flags onto the base deploy options.
func passDeployFlags(base, flags deploy.Options) deploy.Options {
	base.AllowDirty, base.AllowDestructive = flags.AllowDirty, flags.AllowDestructive
	base.SkipBuild, base.NoAutoRollback = flags.SkipBuild, flags.NoAutoRollback
	base.Tag, base.Platform, base.HealthTimeout = flags.Tag, flags.Platform, flags.HealthTimeout
	return base
}

func newDOLogin(app *App) *cobra.Command {
	var noBrowser bool
	c := &cobra.Command{
		Use:   "login",
		Short: "Sign in to DigitalOcean in a browser and store the token",
		Long: "Open the DigitalOcean sign-in page, wait for the answer on a loopback address of this machine,\n" +
			"and store the token. The command needs a browser on this machine.\n" +
			"Use a personal access token in DIGITALOCEAN_ACCESS_TOKEN on a machine with no browser.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := refuseDryRun(app, "login", "run the command without --dry-run"); err != nil {
				return err
			}
			sess, err := newDOSession(app, c, noBrowser)
			if err != nil {
				return err
			}
			// Always sign in again, even when a token is stored.
			src := sess.source
			tok, err := src.Login(c.Context())
			if err != nil {
				return err
			}
			if err := src.Store.WithLock(c.Context(), func() error { return src.Store.Save(tok) }); err != nil {
				return err
			}
			out := c.OutOrStdout()
			var info doInfo
			if len(tok.Info) > 0 {
				_ = json.Unmarshal(tok.Info, &info)
			}
			who := firstNonEmpty(info.Email, info.Name)
			switch {
			case who != "" && info.TeamName != "":
				_, _ = fmt.Fprintf(out, "Signed in to DigitalOcean as %s (team %s)\n", who, info.TeamName)
			case who != "":
				_, _ = fmt.Fprintf(out, "Signed in to DigitalOcean as %s\n", who)
			default:
				_, _ = fmt.Fprintln(out, "Signed in to DigitalOcean")
			}
			return nil
		},
	}
	c.Flags().BoolVar(&noBrowser, "no-browser", false, "print the sign-in URL and do not open a browser")
	return c
}

func newDOLogout(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Revoke the stored DigitalOcean token and delete it",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := refuseDryRun(app, "logout", "run the command without --dry-run"); err != nil {
				return err
			}
			sess, err := newDOSession(app, c, true)
			if err != nil {
				return err
			}
			path := filepath.Join(sess.source.Store.Dir, sess.source.Store.Name)
			_, statErr := os.Stat(path)
			if err := sess.source.Logout(c.Context()); err != nil {
				if errors.Is(err, cloudauth.ErrNotRevoked) {
					return &ui.Error{What: "Removed " + path + ", but the token was not revoked", Why: err.Error(),
						Try: "revoke the spinup access at https://cloud.digitalocean.com/account/api/applications, or wait until the token expires"}
				}
				return err
			}
			out := c.OutOrStdout()
			if statErr != nil {
				_, _ = fmt.Fprintln(out, "No stored DigitalOcean sign-in exists. Nothing to remove.")
			} else {
				_, _ = fmt.Fprintf(out, "Revoked the DigitalOcean token and removed %s\n", path)
			}
			if os.Getenv("DIGITALOCEAN_ACCESS_TOKEN") != "" || os.Getenv("DIGITALOCEAN_TOKEN") != "" {
				_, _ = fmt.Fprintln(out, "A personal access token variable is still set. Unset it to stop using it.")
			}
			return nil
		},
	}
}

func newDOStatus(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the token source, the account, and the resources of the environment",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			sess, err := newDOSession(app, c, true)
			if err != nil {
				return err
			}
			// A status call never starts a browser login.
			sess.source.Interactive = false
			tok, from, err := sess.source.Current(c.Context())
			if err != nil {
				return err
			}
			desc := from
			if from == "store" && !tok.Expiry.IsZero() {
				desc = "store (access token expires " + tok.Expiry.UTC().Format(time.RFC3339) + ")"
			}
			out := c.OutOrStdout()
			if app.Flags.JSON {
				out = c.ErrOrStderr()
			}
			_, _ = fmt.Fprintf(out, "Token source: %s\n", desc)

			if root, err := project.FindRoot(dirOrDot(app.Flags.Dir)); err == nil {
				if _, err := project.Load(root); err == nil {
					opts := doOptions(app, c, provision.Options{})
					return provision.Status(c.Context(), opts, provision.DefaultDeps(sess.client, nil))
				}
			}
			acct, err := sess.client.Account(c.Context())
			if err != nil {
				return &ui.Error{What: "Read the DigitalOcean account", Why: err.Error(), Try: "run spinup deploy digital-ocean login"}
			}
			team, name := acct.UUID, ""
			if acct.Team != nil {
				team, name = acct.Team.UUID, acct.Team.Name
			}
			if app.Flags.JSON {
				enc := json.NewEncoder(c.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{
					"token_source": from,
					"account":      map[string]string{"email": acct.Email, "team": team, "team_name": name},
				})
			}
			_, _ = fmt.Fprintf(out, "Account: %s (team %s)\n", acct.Email, firstNonEmpty(name, team))
			_, _ = fmt.Fprintln(out, "No spinup project found, so no environment is shown.")
			return nil
		},
	}
}

func newDODestroy(app *App) *cobra.Command {
	var iKnow bool
	c := &cobra.Command{
		Use:   "destroy",
		Short: "Delete the droplet, the firewall, and the DNS record of the environment",
		Long: "Delete only the resources that carry the environment tag and the IDs in spinup.toml.\n" +
			"The database on the droplet is lost. Run spinup backup first. The account SSH key and the local secrets stay.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := refuseDryRun(app, "destroy", "run spinup deploy digital-ocean status to see the resources"); err != nil {
				return err
			}
			sess, err := newDOSession(app, c, true)
			if err != nil {
				return err
			}
			opts := doOptions(app, c, provision.Options{IKnow: iKnow})
			return provision.Destroy(c.Context(), opts, provision.DefaultDeps(sess.client, nil))
		},
	}
	c.Flags().BoolVar(&iKnow, "i-know", false, "accept that the command deletes the droplet and its database")
	return c
}

// refuseDryRun stops a subcommand that has no preview mode, so --dry-run never changes anything.
func refuseDryRun(app *App, cmd, try string) error {
	if app.Flags.DryRun {
		return &ui.Error{What: "spinup deploy digital-ocean " + cmd + " has no dry run", Why: "the command has no preview mode, and --dry-run must not change anything", Code: ui.ExitUsage, Try: try}
	}
	return nil
}

func dirOrDot(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
