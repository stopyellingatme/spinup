// Package db operates application migrations through the generated API.
package db

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	run "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// Options controls the migration command and its destructive-operation guards.
type Options struct {
	Dir, Sub, Env string
	// Kit selects the kit migration space (ADR 0028): `migrate kit <Sub> [Version]`.
	Kit bool
	// Version is the target of `migrate kit to`.
	Version                             string
	IKnow, Interactive, Yes, CI, DryRun bool
	// ConfirmName is the value of --confirm-env.
	ConfirmName string
	In          io.Reader
	Out, ErrOut io.Writer
}

// Migrate runs the app's migration command and preserves its exit status.
func Migrate(ctx context.Context, o Options, runner run.Runner) error {
	args, err := migrateArgs(o)
	if err != nil {
		return err
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
		if rollsBack(o) {
			if err := project.ConfirmProtected(env, "db "+strings.Join(args, " "), project.Confirm{In: o.In, Out: o.Out, Interactive: o.Interactive, Yes: o.Yes, CI: o.CI, Name: o.ConfirmName}); err != nil {
				return migrateError(err)
			}
		}
	}
	c := run.Cmd{Name: "go", Args: append([]string{"run", "./cmd/api"}, args...), Dir: filepath.Join(root, "api"), Stdout: o.Out, Stderr: o.ErrOut}
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

// kitSubs are the subcommands of `migrate kit` on the app binary (kit/db/command.go).
var kitSubs = map[string]bool{"up": true, "down": true, "status": true, "to": true, "version": true}

// rollsBack reports whether the command can remove applied migrations.
func rollsBack(o Options) bool {
	return o.Sub == "down" || (o.Kit && o.Sub == "to")
}

// migrateArgs checks the options and returns the arguments of the app binary.
func migrateArgs(o Options) ([]string, error) {
	if !o.Kit {
		if o.Sub == "" {
			o.Sub = "up"
		}
		if o.Sub != "up" && o.Sub != "down" && o.Sub != "status" {
			return nil, &ui.Error{What: "Cannot run migration", Why: "unknown migration command " + o.Sub, Try: "spinup db migrate --help", Code: ui.ExitUsage}
		}
		if o.Sub == "down" && !o.IKnow {
			return nil, &ui.Error{What: "This rolls back the last migration and can lose data", Why: "down requires explicit acknowledgement", Try: "spinup db migrate down --i-know"}
		}
		return []string{"migrate", o.Sub}, nil
	}
	if !kitSubs[o.Sub] {
		why := "no kit migration command given"
		if o.Sub != "" {
			why = "unknown kit migration command " + o.Sub
		}
		return nil, &ui.Error{What: "Cannot run kit migration", Why: why + " (use up, down, status, to <version>, or version)", Try: "spinup db migrate --help", Code: ui.ExitUsage}
	}
	if o.Sub == "to" {
		if _, err := strconv.ParseInt(o.Version, 10, 64); err != nil {
			return nil, &ui.Error{What: "Cannot run kit migration", Why: "to needs a numeric version, such as 5", Try: "spinup db migrate kit to <version> --i-know", Code: ui.ExitUsage}
		}
	} else if o.Version != "" {
		return nil, &ui.Error{What: "Cannot run kit migration", Why: "only to takes a version", Try: "spinup db migrate --help", Code: ui.ExitUsage}
	}
	args := []string{"migrate", "kit", o.Sub}
	if o.Sub == "to" {
		args = append(args, o.Version)
	}
	if rollsBack(o) && !o.IKnow {
		return nil, &ui.Error{What: "This can roll back kit migrations and lose data", Why: o.Sub + " requires explicit acknowledgement", Try: "spinup db " + strings.Join(args, " ") + " --i-know"}
	}
	return args, nil
}

func migrateError(err error) error {
	code := ui.ExitError
	why := err.Error()
	var exit *run.ExitError
	if errors.As(err, &exit) {
		code = exit.Code
		why = fmt.Sprintf("%s exited with status %d", exit.Cmd, exit.Code)
	}
	try := "check the command output and environment, then retry"
	// Errors from internal/project carry their own "Try:" hint. Show it once, in the Try field.
	if before, after, ok := strings.Cut(why, "Try:"); ok {
		why = strings.TrimRight(strings.TrimSpace(before), ".")
		try = strings.TrimSpace(after)
	}
	return &ui.Error{What: "Cannot run migration", Why: why, Try: try, Code: code, Err: err}
}
