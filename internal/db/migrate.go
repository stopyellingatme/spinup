// Package db operates application migrations through the generated API.
package db

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	run "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// Options controls the migration command and its destructive-operation guards.
type Options struct {
	Dir, Sub, Env                       string
	IKnow, Interactive, Yes, CI, DryRun bool
	In                                  io.Reader
	Out, ErrOut                         io.Writer
}

// Migrate runs the app's migration command and preserves its exit status.
func Migrate(ctx context.Context, o Options, runner run.Runner) error {
	if o.Sub == "" {
		o.Sub = "up"
	}
	if o.Sub != "up" && o.Sub != "down" && o.Sub != "status" {
		return &ui.Error{What: "Cannot run migration", Why: "unknown migration command " + o.Sub, Try: "spinup db migrate --help", Code: ui.ExitUsage}
	}
	if o.Sub == "down" && !o.IKnow {
		return &ui.Error{What: "This rolls back the last migration and can lose data", Why: "down requires explicit acknowledgement", Try: "spinup db migrate down --i-know"}
	}
	root, err := project.FindRoot(o.Dir)
	if err != nil {
		return migrateError(err)
	}
	config, err := project.Load(root)
	if err != nil {
		return migrateError(err)
	}
	if len(config.Envs) > 0 || o.Env != "" {
		env, err := config.ResolveEnv(o.Env)
		if err != nil {
			return migrateError(err)
		}
		if o.Sub == "down" {
			if err := project.ConfirmProtected(env, "db migrate "+o.Sub, project.Confirm{In: o.In, Out: o.Out, Interactive: o.Interactive, Yes: o.Yes, CI: o.CI}); err != nil {
				return migrateError(err)
			}
		}
	}
	c := run.Cmd{Name: "go", Args: []string{"run", "./cmd/api", "migrate", o.Sub}, Dir: filepath.Join(root, "api"), Stdout: o.Out, Stderr: o.ErrOut}
	if o.DryRun {
		_, err := fmt.Fprintln(o.Out, c.String())
		return err
	}
	_, err = runner.Run(ctx, c)
	if err != nil {
		return migrateError(err)
	}
	return nil
}

func migrateError(err error) error {
	code := ui.ExitError
	why := err.Error()
	var exit *run.ExitError
	if errors.As(err, &exit) {
		code = exit.Code
		why = fmt.Sprintf("%s exited with status %d", exit.Cmd, exit.Code)
	}
	return &ui.Error{What: "Cannot run migration", Why: why, Try: "check the command output and environment, then retry", Code: code, Err: err}
}
