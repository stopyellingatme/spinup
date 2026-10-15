package deploy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/internal/build"
	"github.com/stopyellingatme/spinup/internal/db/lint"
	"github.com/stopyellingatme/spinup/internal/drift"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/secrets"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// Steps are the journaled steps of a deploy, in order (ADR 0009 §2, architecture §14).
// preflight and build run locally before the lock. The deployer journals them when it takes the lock.
var Steps = []string{"preflight", "build", "hooks", "transport", "upload", "edge", "migrate", "switch", "smoke", "record", "post"}

// DefaultLockTTL is the lifetime of the deploy lock. The deployer refreshes it between steps.
const DefaultLockTTL = 30 * time.Minute

// Options are the inputs of spinup deploy.
type Options struct {
	// Dir selects the project. Env selects the environment in spinup.toml.
	Dir, Env string
	// Plan prints what would change and changes nothing on the VM.
	Plan bool
	// Resume continues the deploy in the journal from its first step that is not done.
	Resume bool
	// AllowDirty permits uncommitted changes. AllowDestructive permits db lint findings.
	AllowDirty, AllowDestructive bool
	// SkipBuild uses images that exist already. SkipBackup goes to the pre-deploy hooks.
	SkipBuild, SkipBackup bool
	// NoAutoRollback leaves an unhealthy new color running.
	NoAutoRollback bool
	// Tag sets the release ID, for images that CI built. It must end with the HEAD SHA.
	Tag string
	// Platform is the image platform (default linux/amd64).
	Platform string
	// HealthTimeout limits the start of the new color (default 120 s).
	HealthTimeout time.Duration
	// LockTTL is the lifetime of the deploy lock (default 30 min).
	LockTTL time.Duration
	// AcceptNewHostKey accepts the host key of a new VM.
	AcceptNewHostKey bool
	// Confirm answers the confirmation of a protected environment.
	Confirm project.Confirm
	// JSON writes the status as JSON.
	JSON bool
	// Out receives progress. ErrOut receives warnings.
	Out, ErrOut io.Writer
}

// Deps are the side effects of the deployer. Tests replace them with fakes.
type Deps struct {
	// Exec runs every local subprocess (git, docker).
	Exec runexec.Runner
	// Dial opens the SSH connection to the environment.
	Dial func(ctx context.Context, env project.Env, acceptNewHostKey bool) (remote.Runner, error)
	// Decrypt returns the secret values of the environment.
	Decrypt func(ctx context.Context, root, env string) (map[string]string, error)
	// Check runs spinup check. Lint runs db lint.
	Check func(ctx context.Context, root string, out io.Writer) error
	Lint  func(root string) ([]lint.Finding, error)
	// Build builds the images.
	Build func(ctx context.Context, r runexec.Runner, o build.ImageOptions) ([]build.Image, error)
	// HTTPGet requests a public URL for the smoke test.
	HTTPGet func(ctx context.Context, url string) (status int, body []byte, err error)
	// Now and Sleep are the clock.
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
	// User names the person who deploys, for release.json and the lock.
	User func() string
	// PreDeploy and PostRelease return the registered hooks.
	PreDeploy, PostRelease func() []Hook
}

// DefaultDeps returns the real side effects.
func DefaultDeps() Deps {
	return Deps{
		Exec: runexec.OSRunner{},
		Dial: func(_ context.Context, env project.Env, accept bool) (remote.Runner, error) {
			host, err := remote.HostFromEnv(env)
			if err != nil {
				return nil, err
			}
			return remote.New(host, runexec.OSRunner{}, remote.Options{AcceptNewHostKey: accept}), nil
		},
		Decrypt: secrets.Decrypt,
		Check: func(ctx context.Context, root string, out io.Writer) error {
			return drift.Check(ctx, drift.Options{Dir: root, Offline: true}, true, ui.New(nil, out, out, ui.Options{NoColor: true, NonInteractive: true}))
		},
		Lint: func(root string) ([]lint.Finding, error) {
			dir := root + "/api/db/migrations"
			if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
				return nil, nil
			}
			return lint.LintDir(dir)
		},
		Build:   build.BuildImages,
		HTTPGet: httpGet,
		Now:     time.Now,
		Sleep:   sleepContext,
		User: func() string {
			if u := os.Getenv("USER"); u != "" {
				return u
			}
			return "unknown"
		},
		PreDeploy:   PreDeployHooks,
		PostRelease: PostReleaseHooks,
	}
}

func (d *Deps) fill() {
	def := DefaultDeps()
	if d.Exec == nil {
		d.Exec = def.Exec
	}
	if d.Dial == nil {
		d.Dial = def.Dial
	}
	if d.Decrypt == nil {
		d.Decrypt = def.Decrypt
	}
	if d.Check == nil {
		d.Check = def.Check
	}
	if d.Lint == nil {
		d.Lint = def.Lint
	}
	if d.Build == nil {
		d.Build = def.Build
	}
	if d.HTTPGet == nil {
		d.HTTPGet = def.HTTPGet
	}
	if d.Now == nil {
		d.Now = def.Now
	}
	if d.Sleep == nil {
		d.Sleep = def.Sleep
	}
	if d.User == nil {
		d.User = def.User
	}
	if d.PreDeploy == nil {
		d.PreDeploy = def.PreDeploy
	}
	if d.PostRelease == nil {
		d.PostRelease = def.PostRelease
	}
}

func (o *Options) fill() {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.ErrOut == nil {
		o.ErrOut = io.Discard
	}
	if o.HealthTimeout <= 0 {
		o.HealthTimeout = DefaultHealthTimeout
	}
	if o.LockTTL <= 0 {
		o.LockTTL = DefaultLockTTL
	}
	if o.Platform == "" {
		o.Platform = "linux/amd64"
	}
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

func httpGet(ctx context.Context, url string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}

// redactor replaces secret values in everything that the deployer writes.
type redactor struct {
	w      io.Writer
	values []string
}

func (r *redactor) Write(p []byte) (int, error) {
	s := r.redact(string(p))
	if _, err := io.WriteString(r.w, s); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (r *redactor) redact(s string) string {
	for _, v := range r.values {
		if v != "" {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return s
}

// redactValues returns the secret values to redact, the longest first, without short values
// that would hide ordinary words.
func redactValues(values map[string]string) []string {
	var out []string
	for _, v := range values {
		if len(v) >= 4 {
			out = append(out, v)
		}
		if t := strings.TrimSpace(v); t != v && len(t) >= 4 {
			out = append(out, t)
		}
	}
	// Replace longer values first, so that a value inside another value cannot leave a partial secret.
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}

// deployError is a step failure with the resume hint.
func deployError(step, env string, err error) error {
	var locked *LockedError
	if errors.As(err, &locked) {
		return err
	}
	var health *HealthError
	try := "correct the problem and run spinup deploy --resume --env " + env
	if errors.As(err, &health) {
		try = "read the logs with spinup server logs --env " + env + ", correct the problem, and run spinup deploy --env " + env
	}
	return &ui.Error{What: "Deploy failed at step " + step, Why: stripTry(err.Error()), Try: try, Err: err}
}

// stripTry removes the Try lines of a wrapped error, so that the user sees one Try line.
func stripTry(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, l := range lines {
		if !strings.HasPrefix(strings.TrimSpace(l), "Try:") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "; ")
}
