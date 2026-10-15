package secrets

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
)

var (
	appPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	envPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	namePattern      = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	recipientPattern = regexp.MustCompile(`^age1[023456789acdefghjklmnpqrstuvwxyz]+$`)
)

// Options supplies project selection, recipients, streams, and confirmation flags.
type Options struct {
	Dir, Env                                 string
	RecoveryRecipient, CIRecipient           string
	NoRecovery, Interactive, Yes, CI, DryRun bool
	In                                       io.Reader
	Out, ErrOut                              io.Writer
}

type environment struct {
	root, app, name, key, path string
	config                     project.Env
}

// KeyPath returns the private key path for an application and environment.
// SPINUP_KEYS_DIR replaces the default directory for tests and automation.
func KeyPath(app, env string) (string, error) {
	if !appPattern.MatchString(app) || !envPattern.MatchString(env) {
		return "", problem("resolve key path", "invalid application or environment name", "use a valid application name and --env")
	}
	dir := os.Getenv("SPINUP_KEYS_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", problem("resolve key path", "the home directory is unavailable", "set SPINUP_KEYS_DIR")
		}
		dir = filepath.Join(home, ".config", "spinup", "keys")
	}
	key, err := filepath.Abs(filepath.Join(dir, app+"-"+env+".agekey"))
	if err != nil {
		return "", problem("resolve key path", "the key directory cannot be resolved", "set SPINUP_KEYS_DIR to an absolute path")
	}
	return key, nil
}

func resolve(o Options) (environment, error) {
	if !envPattern.MatchString(o.Env) {
		return environment{}, problem("select secrets environment", "--env is missing or invalid", "pass --env <environment>")
	}
	dir := o.Dir
	if dir == "" {
		dir = "."
	}
	root, err := project.FindRoot(dir)
	if err != nil {
		return environment{}, problem("find secrets project", "spinup.toml is unavailable", "run inside a spinup project or pass --dir")
	}
	config, err := project.Load(root)
	if err != nil {
		return environment{}, problem("load secrets project", "spinup.toml is invalid", "correct spinup.toml")
	}
	key, err := KeyPath(config.App.Name, o.Env)
	if err != nil {
		return environment{}, err
	}
	env := config.Envs[o.Env]
	env.Name = o.Env
	return environment{root: root, app: config.App.Name, name: o.Env, key: key, path: filepath.Join("secrets", o.Env+".enc.yaml"), config: env}, nil
}

func problem(action, why, hint string) error {
	return fmt.Errorf("%s: %s\nTry: %s", action, why, hint)
}

// runTool intentionally discards subprocess diagnostics. A parser or editor can
// include an unknown secret in an error before redaction values are available.
func runTool(ctx context.Context, r runexec.Runner, c runexec.Cmd) error {
	if r == nil {
		return problem("run secrets tool", "no runner is configured", "configure an exec.Runner")
	}
	if _, err := r.Run(ctx, c); err != nil {
		return problem("run "+c.Name, "the secrets tool failed", "check the installed tool, recipients, and environment key")
	}
	return nil
}

func (e environment) command(args ...string) runexec.Cmd {
	return runexec.Cmd{Name: "sops", Args: args, Dir: e.root, Env: []string{"SOPS_AGE_KEY_FILE=" + e.key, "SOPS_DISABLE_VERSION_CHECK=1"}}
}

func (e environment) requireFile() error {
	info, err := os.Lstat(filepath.Join(e.root, e.path))
	if err != nil || !info.Mode().IsRegular() {
		return problem("read encrypted secrets", "the environment file is missing or is not a regular file", "run spinup secrets init --env "+e.name)
	}
	return nil
}

// Edit starts the interactive sops editor with the environment key.
func Edit(ctx context.Context, o Options, r runexec.Runner) error {
	if !o.Interactive {
		return problem("edit secrets", "a terminal is required", "run in a terminal or use spinup secrets set --env <environment> <NAME>")
	}
	e, err := resolve(o)
	if err != nil {
		return err
	}
	if err = e.requireFile(); err != nil {
		return err
	}
	if o.DryRun {
		return nil
	}
	values, err := e.decrypt(ctx, r, e.path)
	if err != nil {
		return err
	}
	c := e.command("edit", e.path)
	c.Interactive = true
	c.Stdin, c.Stdout, c.Stderr = o.In, o.Out, o.ErrOut
	c.Redact = redactions(values)
	return runTool(ctx, r, c)
}

// Get writes only the requested value to stdout.
func Get(ctx context.Context, o Options, name string, r runexec.Runner) error {
	if !namePattern.MatchString(name) {
		return problem("get secret", "the name is invalid", "use an uppercase environment variable name")
	}
	e, err := resolve(o)
	if err != nil {
		return err
	}
	values, err := e.decrypt(ctx, r, e.path)
	if err != nil {
		return err
	}
	value, ok := values[name]
	if !ok {
		return problem("get secret "+name, "the name is absent", "add the name with spinup secrets set")
	}
	if o.Out == nil {
		return problem("get secret", "stdout is unavailable", "provide an output stream")
	}
	if _, err = io.WriteString(o.Out, value); err != nil {
		return problem("get secret", "stdout could not be written", "check the output stream")
	}
	return nil
}

// Set reads a value from stdin and updates its encrypted entry.
func Set(ctx context.Context, o Options, name string, r runexec.Runner) error {
	if !namePattern.MatchString(name) {
		return problem("set secret", "the name is invalid", "use an uppercase environment variable name")
	}
	e, err := resolve(o)
	if err != nil {
		return err
	}
	if err = e.requireFile(); err != nil {
		return err
	}
	if o.DryRun {
		return nil
	}
	if o.In == nil {
		return problem("set secret", "stdin is unavailable", "pipe the value through stdin")
	}
	value, err := io.ReadAll(o.In)
	if err != nil {
		return problem("set secret", "stdin could not be read", "pipe the value through stdin")
	}
	values, err := e.decrypt(ctx, r, e.path)
	if err != nil {
		return err
	}
	previousRedactions := redactions(values)
	values[name] = string(value)
	encoded := encodeString(string(value))
	c := e.command("set", "--value-stdin", e.path, `["`+name+`"]`)
	c.Stdin = strings.NewReader(encoded)
	c.Redact = redactions(values)
	c.Redact = append(c.Redact, previousRedactions...)
	c.Redact = append(c.Redact, encoded)
	return runTool(ctx, r, c)
}

// Rotate replaces the data key after confirmation for a protected environment.
func Rotate(ctx context.Context, o Options, r runexec.Runner) error {
	return changeKeys(ctx, o, r, "rotate")
}

// Rekey updates recipients from .sops.yaml after confirmation.
func Rekey(ctx context.Context, o Options, r runexec.Runner) error {
	return changeKeys(ctx, o, r, "updatekeys")
}

func changeKeys(ctx context.Context, o Options, r runexec.Runner, action string) error {
	e, err := resolve(o)
	if err != nil {
		return err
	}
	if err = project.ConfirmProtected(e.config, "change secrets keys", project.Confirm{Interactive: o.Interactive, Yes: o.Yes, CI: o.CI, In: o.In, Out: o.ErrOut}); err != nil {
		return problem("confirm secrets change", "protected environment confirmation failed", "run in a terminal or pass --yes in CI")
	}
	if err = e.requireFile(); err != nil {
		return err
	}
	if o.DryRun {
		return nil
	}
	values, err := e.decrypt(ctx, r, e.path)
	if err != nil {
		return err
	}
	args := []string{action, "--in-place", e.path}
	if action == "updatekeys" {
		args = []string{action, "--yes", e.path}
	}
	c := e.command(args...)
	c.Redact = redactions(values)
	return runTool(ctx, r, c)
}

func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return problem("create key directory", "the directory could not be created", "check SPINUP_KEYS_DIR and its parent permissions")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return problem("secure key directory", "the key directory is not a regular directory", "use a private directory in SPINUP_KEYS_DIR")
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return problem("secure key directory", "permissions could not be changed", "set the key directory mode to 0700")
	}
	return nil
}

func absent(path string) error {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return problem("initialize secrets", "a target already exists or cannot be inspected", "use the existing environment key and secrets file")
}
