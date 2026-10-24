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
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/ui"
	"github.com/stopyellingatme/spinup/internal/webtool"
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
	lock, err := loadBuildLock(root)
	if err != nil {
		return buildError(err)
	}
	if embedsWeb(lock) {
		if err := buildWeb(ctx, root, lock, o, runner); err != nil {
			return buildError(err)
		}
		c.Args = append([]string{"build", "-tags", "embedweb"}, c.Args[1:]...)
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

// loadBuildLock preserves builds for older projects without a lock.
func loadBuildLock(root string) (*manifest.Lock, error) {
	lock, err := manifest.Load(filepath.Join(root, project.LockFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return lock, err
}

func embedsWeb(lock *manifest.Lock) bool {
	return lock != nil && lock.Answers["frontend_mode"] == "embed"
}

func buildWeb(ctx context.Context, root string, lock *manifest.Lock, o Options, runner run.Runner) error {
	tool := webtool.FromLock(lock)
	for _, c := range []run.Cmd{tool.Install(true, false), tool.Run("build")} {
		c = tool.InWeb(root, c)
		c.Stdout, c.Stderr = o.Out, o.ErrOut
		if o.DryRun {
			if _, err := fmt.Fprintln(o.Out, c.String()); err != nil {
				return err
			}
		} else if _, err := runner.Run(ctx, c); err != nil {
			return err
		}
	}
	if o.DryRun {
		_, err := fmt.Fprintln(o.Out, "Copy web/build to api/internal/webdist/dist, replacing old assets")
		return err
	}
	source := filepath.Join(root, "web", "build")
	dest := filepath.Join(root, "api", "internal", "webdist", "dist")
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("read web/build: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("copy web/build: the build output is not a directory")
	}
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("replace api/internal/webdist/dist: %w", err)
	}
	if err := os.CopyFS(dest, os.DirFS(source)); err != nil {
		return fmt.Errorf("copy web/build to api/internal/webdist/dist: %w", err)
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
	_, ok := localKitTarget(data)
	return ok
}

// localKitTarget returns the local path that a replace directive gives to the kit.
func localKitTarget(data string) (string, bool) {
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
					return target, true
				}
			}
		}
	}
	return "", false
}

// KitReplaceTarget returns the local kit replace path of go.mod in apiDir as written.
// It reports false when there is no local kit replace.
func KitReplaceTarget(apiDir string) (string, bool, error) {
	data, err := os.ReadFile(filepath.Join(apiDir, "go.mod"))
	if err != nil {
		return "", false, &ui.Error{What: "Cannot read the kit replace", Why: err.Error(), Try: "check api/go.mod and run the command again", Err: err}
	}
	target, ok := localKitTarget(string(data))
	return target, ok, nil
}

// KitReplaceDir returns the absolute kit directory when go.mod in apiDir replaces the kit
// with a local path. A relative path is relative to apiDir. It reports false when there is
// no local replace. It fails when the directory does not exist.
func KitReplaceDir(apiDir string) (string, bool, error) {
	data, err := os.ReadFile(filepath.Join(apiDir, "go.mod"))
	if err != nil {
		return "", false, &ui.Error{What: "Cannot read the kit replace", Why: err.Error(), Try: "check api/go.mod and run the command again", Err: err}
	}
	target, ok := localKitTarget(string(data))
	if !ok {
		return "", false, nil
	}
	dir := filepath.FromSlash(target)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(apiDir, dir)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", false, &ui.Error{What: "Cannot resolve the kit replace", Why: err.Error(), Try: "fix the replace path in api/go.mod", Err: err}
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		why := "the directory " + dir + " does not exist or is not a directory"
		return "", false, &ui.Error{What: "Cannot use the local kit replace in api/go.mod", Why: why, Try: "point the replace at the kit directory or remove it and use a published kit version", Err: err}
	}
	return dir, true, nil
}
