package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/db/lint"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func init() {
	regMu.Lock()
	defer regMu.Unlock()
	for index := range registry {
		build := registry[index].build
		registry[index].build = func(app *App) *cobra.Command {
			command := build(app)
			if command.Name() == "db" {
				command.AddCommand(newDBLint(app))
			}
			return command
		}
	}
}

func newDBLint(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "lint [file...]",
		Short: "Check migrations for unsafe deploy changes",
		Args:  cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			root, err := project.FindRoot(app.Flags.Dir)
			if err != nil {
				return &ui.Error{What: "Cannot lint migrations", Why: err.Error(), Try: "run this inside a spinup project, or pass --dir"}
			}
			findings, err := dbLint(root, args)
			if err != nil {
				return &ui.Error{What: "Cannot lint migrations", Why: err.Error(), Try: "check the migration files and try again"}
			}
			if app.Flags.JSON {
				if err := app.UI.JSON(findings); err != nil {
					return &ui.Error{What: "Cannot write lint findings", Why: err.Error(), Try: "run the command again"}
				}
			} else {
				for _, finding := range findings {
					app.UI.Printf("%s:%d: %s: %s\n  Try: %s\n", finding.File, finding.Line, finding.Rule, finding.Message, finding.Try)
				}
			}
			if len(findings) != 0 {
				return &ui.Error{What: "Database lint failed", Why: fmt.Sprintf("found %d unsafe migration changes", len(findings)), Try: "split the contract step into a later release"}
			}
			return nil
		},
	}
}

func dbLint(root string, files []string) ([]lint.Finding, error) {
	if len(files) == 0 {
		return lint.LintDir(filepath.Join(root, "api", "db", "migrations"))
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if !filepath.IsAbs(file) {
			file = filepath.Join(root, file)
		}
		paths = append(paths, file)
	}
	sort.Strings(paths)
	var findings []lint.Finding
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", path, err)
		}
		got, err := lint.Lint(filepath.Base(path), data)
		if err != nil {
			return nil, err
		}
		findings = append(findings, got...)
	}
	return findings, nil
}
