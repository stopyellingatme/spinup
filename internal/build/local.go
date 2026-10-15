// Package build builds generated applications through the command runner.
package build

import (
	"bufio"
	"context"
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
	Dir, Target, Tag string
	Image, DryRun    bool
	Out, ErrOut      io.Writer
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
		data, err := os.ReadFile(filepath.Join(c.Dir, "go.mod"))
		if err != nil {
			return buildError(err)
		}
		if localKitReplace(string(data)) {
			return &ui.Error{What: "Cannot build the API image", Why: "a local kit replace is outside the Docker build context", Try: "remove the local kit replace from api/go.mod and use a published kit version"}
		}
		config, err := project.Load(root)
		if err != nil {
			return buildError(err)
		}
		c.Name = "docker"
		c.Args = []string{"build", "-f", "../deploy/Dockerfile.api", "-t", config.App.Name + "-api:" + o.Tag, "--build-arg", "VERSION=" + o.Tag, "."}
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
