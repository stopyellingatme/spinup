// Package apptest runs the tests of a generated application.
package apptest

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// Options selects the tests and their output streams.
type Options struct {
	// Root is the project directory.
	Root string
	// Run selects tests by regular expression.
	Run string
	// Race and Cover enable Go test diagnostics.
	Race, Cover bool
	// Web, E2E, and Contract select targets deferred to M4.
	Web, E2E, Contract bool
	// DryRun prints the command without execution.
	DryRun bool
	// Out and ErrOut receive the subprocess output.
	Out, ErrOut io.Writer
}

// Run executes API tests and preserves their exit code.
func Run(ctx context.Context, o Options, runner exec.Runner) error {
	for _, target := range []struct {
		name     string
		selected bool
	}{{"web", o.Web}, {"e2e", o.E2E}, {"contract", o.Contract}} {
		if target.selected {
			return fmt.Errorf("cannot run %s tests: this target is unavailable\nTry: use --api until M4 adds %s tests", target.name, target.name)
		}
	}
	args := []string{"test"}
	if o.Run != "" {
		args = append(args, "-run", o.Run)
	}
	if o.Race {
		args = append(args, "-race")
	}
	if o.Cover {
		args = append(args, "-cover")
	}
	args = append(args, "./...")
	cmd := exec.Cmd{Name: "go", Args: args, Dir: filepath.Join(o.Root, "api"), Stdout: o.Out, Stderr: o.ErrOut}
	if o.DryRun {
		if o.Out == nil {
			return nil
		}
		_, err := fmt.Fprintln(o.Out, cmd.String())
		return err
	}
	if runner == nil {
		runner = exec.OSRunner{}
	}
	result, err := runner.Run(ctx, cmd)
	if err != nil || result.ExitCode != 0 {
		code := result.ExitCode
		if code <= 0 {
			code = ui.ExitError
		}
		why := fmt.Sprintf("go test exited with code %d", code)
		if err != nil {
			why = err.Error()
		}
		return &ui.Error{What: "Application tests failed", Why: why, Try: "correct the test errors, then run spinup test", Code: code, Err: err}
	}
	return nil
}
