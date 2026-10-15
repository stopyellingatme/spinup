package drift

import (
	"context"
	"os"

	"github.com/stopyellingatme/spinup/internal/doctor"
	_ "github.com/stopyellingatme/spinup/internal/doctor/checks" // Register the local checks for command callers.
	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// Doctor runs the registered local checks, including outside an application.
func Doctor(ctx context.Context, o Options, quick bool, runner exec.Runner, out *ui.Printer) error {
	dir := o.Dir
	if dir == "" {
		dir = "."
	}
	if _, err := os.Stat(dir); err != nil {
		return diskError(err)
	}
	root, _ := find(dir)
	reports := doctor.RunAll(ctx, doctor.Local, &doctor.Context{Root: root, Quick: quick, Deps: map[string]any{"cli_version": o.Version, "runner": runner}})
	type report struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Message string `json:"message"`
		Try     string `json:"try,omitempty"`
	}
	result := make([]report, 0, len(reports))
	failed := false
	for _, r := range reports {
		result = append(result, report{r.Check.ID, r.Result.Status.String(), r.Result.Message, r.Result.Try})
		if r.Result.Status == doctor.Fail {
			failed = true
		}
		if !o.JSON {
			out.Printf("%s\t%s\t%s\n", r.Result.Status, r.Check.ID, r.Result.Message)
			if r.Result.Try != "" {
				out.Printf("Try: %s\n", r.Result.Try)
			}
		}
	}
	if o.JSON {
		if err := out.JSON(result); err != nil {
			return err
		}
	}
	if failed {
		return &ui.Error{What: "Doctor checks failed", Why: "a local health check failed", Try: "correct the reported failures and run spinup doctor", Code: ui.ExitError}
	}
	return nil
}
