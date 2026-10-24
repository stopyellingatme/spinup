package provision

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean"
	"github.com/stopyellingatme/spinup/internal/deploy"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/secrets"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// DefaultEnvName is the environment name when neither --env nor default_env gives one.
const DefaultEnvName = "production"

// deployUser is the SSH user that cloud-init creates on the droplet.
const deployUser = "deploy"

var envNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Options are the inputs of spinup deploy digital-ocean and its subcommands.
type Options struct {
	// Dir selects the project. Env selects the environment in spinup.toml.
	Dir, Env string
	// Region, Size, and Image select the droplet. Empty values take the stored value, then the default.
	Region, Size, Image string
	// Domain is the host name of the app. PublicURL replaces the public URL.
	Domain, PublicURL string
	// SSHKey is the operator public key file. SSHFrom is the comma list of sources for port 22.
	SSHKey, SSHFrom string
	// RecoveryRecipient and NoRecovery answer the recovery question of secrets init.
	RecoveryRecipient string
	NoRecovery        bool
	// Plan prints the changes and changes nothing. SkipDeploy stops before the deploy.
	Plan, SkipDeploy bool
	// NoCommit does not commit the provisioning files.
	NoCommit bool
	// Recreate deletes the droplet and makes a new one. IKnow permits the loss of data.
	Recreate, IKnow bool
	// DNSTimeout limits the wait for a DNS record that DigitalOcean does not hold (default 10 min).
	DNSTimeout time.Duration
	// Deploy holds the pass-through deploy flags. Dir, Env, Out, and ErrOut come from this package.
	Deploy deploy.Options
	// Confirm answers the price question and the destroy confirmation.
	Confirm project.Confirm
	// JSON writes the status as JSON.
	JSON bool
	// Out receives progress. ErrOut receives warnings.
	Out, ErrOut io.Writer
}

// Secrets is the part of the secrets package that provisioning uses.
type Secrets interface {
	// Exists reports whether secrets/<env>.enc.yaml exists.
	Exists(root, env string) bool
	Init(ctx context.Context, o secrets.Options) error
	Decrypt(ctx context.Context, root, env string) (map[string]string, error)
	// Set stores one value. The value goes through stdin, never through argv.
	Set(ctx context.Context, o secrets.Options, name, value string) error
}

type osSecrets struct{ r runexec.Runner }

// DefaultSecrets returns Secrets that use the secrets package with r.
func DefaultSecrets(r runexec.Runner) Secrets { return osSecrets{r: r} }

func (osSecrets) Exists(root, env string) bool {
	info, err := os.Stat(filepath.Join(root, "secrets", env+".enc.yaml"))
	return err == nil && !info.IsDir()
}

func (s osSecrets) Init(ctx context.Context, o secrets.Options) error {
	return secrets.Init(ctx, o, s.r)
}

func (osSecrets) Decrypt(ctx context.Context, root, env string) (map[string]string, error) {
	return secrets.Decrypt(ctx, root, env)
}

func (s osSecrets) Set(ctx context.Context, o secrets.Options, name, value string) error {
	o.In = strings.NewReader(value)
	return secrets.Set(ctx, o, name, s.r)
}

// Deps are the side effects of provisioning. Tests replace them with fakes.
type Deps struct {
	// Exec runs every local subprocess (git, docker, ssh-keygen).
	Exec runexec.Runner
	// Cloud is the DigitalOcean API client.
	Cloud *digitalocean.Client
	// Dial opens the SSH connection to the environment. The env carries the pinned host keys.
	Dial func(ctx context.Context, env project.Env) (remote.Runner, error)
	// Deploy runs the normal deploy.
	Deploy  func(ctx context.Context, o deploy.Options) error
	Secrets Secrets
	// Resolve looks up the addresses of a host name.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// Now and Sleep are the clock.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
	// LookPath finds a tool on PATH.
	LookPath func(string) (string, error)
}

// DefaultDeps returns the real side effects.
func DefaultDeps(cloud *digitalocean.Client, deployFn func(ctx context.Context, o deploy.Options) error) Deps {
	r := runexec.OSRunner{}
	return Deps{
		Exec:  r,
		Cloud: cloud,
		Dial: func(_ context.Context, env project.Env) (remote.Runner, error) {
			host, err := remote.HostFromEnv(env)
			if err != nil {
				return nil, err
			}
			return remote.New(host, r, remote.Options{}), nil
		},
		Deploy:  deployFn,
		Secrets: DefaultSecrets(r),
		Resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		Now:      time.Now,
		Sleep:    sleepContext,
		LookPath: runexec.LookPath,
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// fill replaces missing dependencies with the real ones.
func (d *Deps) fill() {
	def := DefaultDeps(d.Cloud, d.Deploy)
	if d.Exec == nil {
		d.Exec = def.Exec
	}
	if d.Dial == nil {
		d.Dial = def.Dial
	}
	if d.Secrets == nil {
		d.Secrets = DefaultSecrets(d.Exec)
	}
	if d.Resolve == nil {
		d.Resolve = def.Resolve
	}
	if d.Now == nil {
		d.Now = def.Now
	}
	if d.Sleep == nil {
		d.Sleep = def.Sleep
	}
	if d.LookPath == nil {
		d.LookPath = def.LookPath
	}
}

// fail makes a user-facing error.
func fail(what, why, try string) error {
	return &ui.Error{What: what, Why: why, Try: try}
}

// failWrap makes a user-facing error that wraps err.
// failWrap keeps one Try line. It removes the Try line of err from the reason. The Try line of
// an API error names the cause (sign in again, a missing scope), so it replaces try.
func failWrap(what string, err error, try string) error {
	why := err.Error()
	if i := strings.Index(why, "Try:"); i >= 0 {
		inner := strings.TrimSpace(why[i+len("Try:"):])
		why = strings.TrimRight(why[:i], " .\n")
		var ae *digitalocean.APIError
		if errors.As(err, &ae) && inner != "" {
			try = inner
		}
	}
	return &ui.Error{What: what, Why: why, Try: try, Err: err}
}

// base holds what every command of this package reads first.
type base struct {
	o      Options
	d      Deps
	root   string
	cfg    *project.Config
	app    string
	env    string
	envCfg project.Env
	st     stored
}

func (b *base) say(format string, args ...any) {
	if b.o.Out != nil {
		_, _ = fmt.Fprintf(b.o.Out, format+"\n", args...)
	}
}

func (b *base) warn(format string, args ...any) {
	if b.o.ErrOut != nil {
		_, _ = fmt.Fprintf(b.o.ErrOut, format+"\n", args...)
	}
}

func (b *base) tag() string { return "spinup:" + b.app + ":" + b.env }

func (b *base) firewallName() string { return "spinup-" + b.app + "-" + b.env }

// load finds the project, picks the environment, and reads the stored state.
func (b *base) load() error {
	dir := b.o.Dir
	if dir == "" {
		dir = "."
	}
	root, err := project.FindRoot(dir)
	if err != nil {
		return failWrap("find the project", err, "run this command inside a spinup project, or pass --dir")
	}
	cfg, err := project.Load(root)
	if err != nil {
		return failWrap("load spinup.toml", err, "correct spinup.toml")
	}
	env := b.o.Env
	if env == "" {
		env = cfg.DefaultEnv
	}
	if env == "" {
		env = DefaultEnvName
	}
	if !envNamePattern.MatchString(env) {
		return fail("select the environment", fmt.Sprintf("the name %q must match %s", env, envNamePattern), "pass --env with a lowercase name such as production")
	}
	b.root, b.cfg, b.app, b.env = root, cfg, cfg.App.Name, env
	b.envCfg = cfg.Envs[env]
	b.envCfg.Name = env
	st, err := readStored(root, env)
	if err != nil {
		return err
	}
	b.st = st
	return nil
}

func needCloud(d Deps) error {
	if d.Cloud == nil {
		return fail("use the DigitalOcean API", "no API client is configured", "run spinup deploy digital-ocean login")
	}
	return nil
}

func isNotFound(err error) bool {
	var ae *digitalocean.APIError
	return errors.As(err, &ae) && ae.Status == 404
}
