// Package build builds generated applications through the command runner.
package build

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	run "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// Options selects the application and local build output.
type Options struct {
	Dir, Target, Tag        string
	Platform, Registry, Env string
	Image, Push, DryRun     bool
	Out, ErrOut             io.Writer
}

// Local builds the API or prints the command when DryRun is true.
func Local(ctx context.Context, o Options, runner run.Runner) error {
	if o.Target == "" {
		o.Target = "api"
	}
	if o.Target != "api" && o.Target != "web" && o.Target != "all" {
		return &ui.Error{What: "Cannot build target", Why: "unknown target " + o.Target, Try: "pass --target api or --target all"}
	}
	if o.Target == "web" {
		return &ui.Error{What: "Cannot build the web frontend", Why: "the web pack is not available in this version", Try: "pass --target api"}
	}
	root, err := project.FindRoot(o.Dir)
	if err != nil {
		return buildError(err)
	}
	if o.Tag == "" {
		o.Tag = "dev"
	}
	c := run.Cmd{Name: "go", Args: []string{"build", "-trimpath", "-ldflags", "-X main.version=" + o.Tag, "-o", "bin/api", "./cmd/api"}, Dir: filepath.Join(root, "api"), Stdout: o.Out, Stderr: o.ErrOut}
	if o.Image {
		_, err := Images(ctx, o, runner)
		if err == nil && !o.Push && o.Out != nil {
			_, err = fmt.Fprintln(o.Out, imageWarning(ImageOptions{}))
		}
		return err
	}
	if o.Target == "all" {
		if _, err := fmt.Fprintln(o.Out, "Building available targets: api"); err != nil {
			return buildError(err)
		}
	}
	if o.DryRun {
		_, err := fmt.Fprintln(o.Out, c.String())
		return err
	}
	_, err = runner.Run(ctx, c)
	if err != nil {
		return buildError(err)
	}
	return nil
}

// Images builds application images and returns their deploy references.
func Images(ctx context.Context, o Options, runner run.Runner) ([]Image, error) {
	root, err := project.FindRoot(o.Dir)
	if err != nil {
		return nil, buildError(err)
	}
	config, err := project.Load(root)
	if err != nil {
		return nil, buildError(err)
	}
	publicOrigin := ""
	if o.Env != "" || config.DefaultEnv != "" || len(config.Envs) == 1 {
		env, err := config.ResolveEnv(o.Env)
		if err != nil {
			return nil, buildError(err)
		}
		publicOrigin = env.PublicURL
	}
	if o.DryRun {
		runner = imageDryRunRunner{out: o.Out}
	}
	return BuildImages(ctx, runner, ImageOptions{
		Root: root, App: config.App.Name, Tag: o.Tag, Platform: o.Platform,
		Push: o.Push, Registry: o.Registry, PublicOrigin: publicOrigin,
	})
}

type imageDryRunRunner struct{ out io.Writer }

func (r imageDryRunRunner) Run(_ context.Context, c run.Cmd) (run.Result, error) {
	if _, err := fmt.Fprintln(r.out, c.String()); err != nil {
		return run.Result{}, err
	}
	for index, arg := range c.Args {
		if arg != "--metadata-file" {
			continue
		}
		data, err := json.Marshal(map[string]string{"containerimage.digest": "dry-run"})
		if err != nil {
			return run.Result{}, err
		}
		if err := os.WriteFile(c.Args[index+1], data, 0600); err != nil {
			return run.Result{}, err
		}
	}
	return run.Result{}, nil
}

func buildError(err error) error {
	var missing *run.NotFoundError
	if errors.As(err, &missing) {
		return &ui.Error{What: "Cannot build the API", Why: missing.Error(), Try: "install " + missing.Name + " and run spinup build again", Err: err}
	}
	return &ui.Error{What: "Cannot build the API", Why: err.Error(), Try: "check the project and build output, then run spinup build again", Err: err}
}

// localKitReplace recognizes single-line and grouped Go replace directives.
func localKitReplace(data string) bool {
	scanner := bufio.NewScanner(strings.NewReader(data))
	group := false
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "replace" {
			fields = fields[1:]
			if len(fields) > 0 && fields[0] == "(" {
				group = true
				fields = fields[1:]
			}
		} else if !group {
			continue
		}
		if len(fields) > 0 && fields[0] == ")" {
			group = false
			continue
		}
		if len(fields) == 0 || strings.Trim(fields[0], "\"`") != "github.com/stopyellingatme/spinup/kit" {
			continue
		}
		for i, field := range fields {
			if field == "=>" && i+1 < len(fields) {
				target := strings.Trim(fields[i+1], "\"`")
				if filepath.IsAbs(target) || strings.HasPrefix(target, ".") || strings.Contains(target, ":") || strings.HasPrefix(target, "\\") {
					return true
				}
			}
		}
	}
	return false
}
