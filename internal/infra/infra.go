package infra

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/stopyellingatme/spinup/internal/codemod"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/tools"
)

const tofuDir = "deploy/infra/tofu"

var envPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Options selects an infrastructure operation and its environment.
type Options struct {
	Dir, Env, Action    string
	IKnow, DryRun, JSON bool
	Confirm             project.Confirm
	Out, ErrOut         io.Writer
}

// Deps supplies the runner and environment access for an infrastructure operation.
type Deps struct {
	Runner   runexec.Runner
	LookPath func(string) (string, error)
	Getenv   func(string) string
}

// Run initializes the environment state and runs an OpenTofu operation.
// A successful apply updates only the selected environment's host in spinup.toml.
func Run(ctx context.Context, o Options, d Deps) error {
	switch o.Action {
	case "plan", "apply", "output", "destroy":
	default:
		return errors.New("run infrastructure action: unknown action\nTry: use spinup infra plan, apply, output, or destroy")
	}
	if !envPattern.MatchString(o.Env) {
		return errors.New("select infrastructure environment: --env is missing or invalid\nTry: pass --env <name> with a lowercase environment name")
	}
	if o.Dir == "" {
		o.Dir = "."
	}
	root, err := project.FindRoot(o.Dir)
	if err != nil {
		return failure("find infrastructure project", err)
	}
	cfg, err := project.Load(root)
	if err != nil {
		return failure("load infrastructure configuration", err)
	}
	env, err := cfg.ResolveEnv(o.Env)
	if err != nil {
		return err
	}
	info, err := os.Stat(filepath.Join(root, tofuDir))
	if err != nil || !info.IsDir() {
		return errors.New("locate infrastructure files: the tofu pack is missing\nTry: run spinup module add tofu")
	}
	if o.Action == "destroy" && !o.DryRun {
		if !o.IKnow {
			return errors.New("destroy infrastructure refused: --i-know is missing\nTry: review spinup infra plan, then pass --i-know")
		}
		if err := project.ConfirmProtected(env, "destroy infrastructure", o.Confirm); err != nil {
			return failure("confirm infrastructure destruction", err)
		}
	}
	action := o.Action
	if o.DryRun && (action == "apply" || action == "destroy") {
		action = "plan"
	}
	if (action == "apply" || action == "destroy") && !o.Confirm.Yes && !o.Confirm.Interactive {
		return errors.New("approve infrastructure changes: no terminal is available\nTry: review spinup infra plan, then pass --yes")
	}
	if d.Getenv == nil {
		d.Getenv = os.Getenv
	}
	token := d.Getenv("HCLOUD_TOKEN")
	if strings.TrimSpace(token) == "" {
		return errors.New("run OpenTofu: HCLOUD_TOKEN is missing\nTry: export HCLOUD_TOKEN and run spinup tools install tofu")
	}
	if d.LookPath == nil {
		d.LookPath = toolPath
	}
	binary, err := d.LookPath("tofu")
	if err != nil {
		return fmt.Errorf("locate OpenTofu: %w\nTry: run spinup tools install tofu", err)
	}
	if d.Runner == nil {
		d.Runner = runexec.OSRunner{}
	}
	dataDir := filepath.Join(root, tofuDir, ".terraform", env.Name)
	command := runexec.Cmd{Name: binary, Dir: root, Env: []string{"HCLOUD_TOKEN=" + token, "TF_DATA_DIR=" + dataDir, "TF_WORKSPACE=default"}, Redact: []string{token}, Stdin: o.Confirm.In, Stdout: o.Out, Stderr: o.ErrOut, Interactive: o.Confirm.Interactive}
	run := func(args ...string) (runexec.Result, error) {
		cmd := command
		cmd.Args = append([]string{"-chdir=" + tofuDir}, args...)
		result, err := d.Runner.Run(ctx, cmd)
		if err != nil {
			return result, failure("run OpenTofu "+args[0], err)
		}
		return result, nil
	}
	if _, err := os.Stat(filepath.Join(dataDir, "terraform.tfstate")); err != nil {
		if !os.IsNotExist(err) {
			return failure("inspect OpenTofu initialization", err)
		}
		if _, err := run("init", "-input=false", "-backend-config=path=terraform."+env.Name+".tfstate"); err != nil {
			return err
		}
	}
	args := []string{action}
	if action != "output" && (action == "plan" || o.Confirm.Yes || !o.Confirm.Interactive) {
		args = append(args, "-input=false")
	}
	if action == "plan" && o.Action == "destroy" {
		args = append(args, "-destroy")
	}
	if (action == "apply" || action == "destroy") && o.Confirm.Yes {
		args = append(args, "-auto-approve")
	}
	if action == "output" && o.JSON {
		args = append(args, "-json")
	}
	if _, err := run(args...); err != nil {
		return err
	}
	if action != "apply" {
		return nil
	}
	command.Stdout = nil
	result, err := run("output", "-raw", "ipv4")
	if err != nil {
		return err
	}
	host := strings.TrimSpace(string(result.Stdout))
	address, err := netip.ParseAddr(host)
	if err != nil || !address.Is4() {
		return errors.New("read OpenTofu ipv4 output: the output is not an IPv4 address\nTry: inspect spinup infra output and correct outputs.tf")
	}
	return updateHost(root, env.Name, host)
}

func toolPath(name string) (string, error) {
	if path, ok := tools.Path(name); ok {
		return path, nil
	}
	return runexec.LookPath(name)
}

func updateHost(root, env, host string) error {
	path := filepath.Join(root, project.ConfigFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return failure("read spinup.toml after apply", err)
	}
	updated, err := codemod.SetTOML(data, []string{"env", env}, "host", host)
	if err != nil {
		return failure("update infrastructure host", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return failure("inspect spinup.toml", err)
	}
	file, err := os.CreateTemp(root, ".spinup-infra-*")
	if err != nil {
		return failure("create updated spinup.toml", err)
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(updated); err != nil {
		_ = file.Close()
		return failure("write infrastructure host", err)
	}
	if err := file.Chmod(info.Mode().Perm()); err != nil {
		_ = file.Close()
		return failure("preserve spinup.toml permissions", err)
	}
	if err := file.Close(); err != nil {
		return failure("close updated spinup.toml", err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return failure("save infrastructure host", err)
	}
	return nil
}

func failure(action string, err error) error {
	return fmt.Errorf("%s failed: %w\nTry: check spinup.toml and deploy/infra/tofu, then retry", action, err)
}
