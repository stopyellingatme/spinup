package newapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	run "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/ui"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// Apply writes the application, its local environment, and its lock.
// Git failures produce warnings and leave the generated application available.
func Apply(ctx context.Context, result *Result, o Options, runner run.Runner, printer *ui.Printer) error {
	for _, warning := range result.Warnings {
		printer.Warnf("%s", warning)
	}
	if result.KitReplace != "" {
		printer.Infof("Note: using local kit at %s", result.KitReplace)
	}
	if o.DryRun {
		if !o.JSON {
			for _, file := range result.Plan.Sorted().Files {
				printer.Printf("%s (%s)\n", file.Path, file.Class)
			}
			printer.Println("spinup.lock (CLI-owned)")
			printer.Println(".env (local)")
		}
	} else {
		if err := checkTarget(result.Dir); err != nil {
			return err
		}
		if err := result.Lock.Validate(); err != nil {
			return err
		}
		if err := os.MkdirAll(result.Dir, 0755); err != nil {
			return creationError(err)
		}
		if _, err := vfs.Apply(result.Dir, result.Plan, vfs.ApplyOptions{}); err != nil {
			return creationError(err)
		}
		if err := copyEnvironment(result.Dir); err != nil {
			return creationError(err)
		}
		if err := result.Lock.Save(filepath.Join(result.Dir, "spinup.lock")); err != nil {
			return err
		}
		if !o.NoGit {
			initializeGit(ctx, result.Dir, o.Version, runner, printer)
		}
	}
	if o.JSON {
		return printer.JSON(struct {
			Dir   string   `json:"dir"`
			Packs []string `json:"packs"`
			Files int      `json:"files"`
		}{result.Dir, result.Packs, len(result.Plan.Files)})
	}
	if o.DryRun {
		return nil
	}
	printer.Printf("Created %s (%d files) from packs %s\n", o.Name, len(result.Plan.Files), strings.Join(result.Packs, ", "))
	printer.Printf("\nNext:\n  cd %s\n  spinup dev\n", o.Name)
	return nil
}

func copyEnvironment(root string) error {
	data, err := os.ReadFile(filepath.Join(root, ".env.example"))
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(root, ".env"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func initializeGit(ctx context.Context, dir, version string, runner run.Runner, printer *ui.Printer) {
	if version == "" {
		version = "dev"
	}
	commands := [][]string{{"init", "-b", "main"}, {"add", "-A"}, {"commit", "-m", fmt.Sprintf("Initial commit from spinup %s", version)}}
	for _, args := range commands {
		if _, err := runner.Run(ctx, run.Cmd{Name: "git", Args: args, Dir: dir}); err != nil {
			printer.Warnf("Git initialization did not finish: %v. Try: configure git and commit the app manually.", err)
			return
		}
	}
}
