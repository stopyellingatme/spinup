package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/docsite"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates/store"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func init() { Register(GroupMaintain, newDocs) }

func newDocs(app *App) *cobra.Command {
	command := &cobra.Command{Use: "docs", Short: "Print compatibility data and generate command references", Args: cobra.NoArgs}
	command.AddCommand(newDocsCompat(app), newDocsReference(app))
	return command
}

func newDocsCompat(app *App) *cobra.Command {
	var markdown bool
	command := &cobra.Command{
		Use:   "compat",
		Short: "Print compatible CLI, kit, template, and stack versions",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if markdown && app.Flags.JSON {
				return &ui.Error{What: "Invalid output flags", Why: "--json and --markdown cannot be used together", Try: "use one output flag", Code: ui.ExitUsage}
			}
			matrix, err := compatibility(c, app)
			if err != nil {
				return err
			}
			if app.Flags.JSON {
				return app.UI.JSON(matrix)
			}
			app.UI.Printf("%s", matrix.Markdown())
			return nil
		},
	}
	command.Flags().BoolVar(&markdown, "markdown", false, "write the compatibility matrix as Markdown")
	return command
}

func newDocsReference(app *App) *cobra.Command {
	var out string
	command := &cobra.Command{
		Use:   "reference",
		Short: "Write a Markdown reference page for every command",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if out == "" {
				return &ui.Error{What: "Cannot write command reference", Why: "--out is required", Try: "spinup docs reference --out <directory>", Code: ui.ExitUsage}
			}
			if err := writePages(out, docsite.ReferencePages(c.Root())); err != nil {
				return &ui.Error{What: "Cannot write command reference", Why: err.Error(), Try: "check --out and its parent directory", Code: ui.ExitError}
			}
			return nil
		},
	}
	command.Flags().StringVar(&out, "out", "", "directory for generated Markdown pages")
	return command
}

func compatibility(c *cobra.Command, app *App) (docsite.Compatibility, error) {
	source, err := store.Default(Version, app.Flags.Offline)
	if err != nil {
		return docsite.Compatibility{}, err
	}
	versions, err := source.Versions(c.Context())
	if err != nil {
		return docsite.Compatibility{}, err
	}
	return docsite.NewCompatibility(Version, versions, stackversions.MustLoad()), nil
}

func writePages(root string, pages map[string][]byte) error {
	paths := make([]string, 0, len(pages))
	for path := range pages {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if filepath.IsAbs(path) || path == ".." || len(path) > 2 && path[:3] == "../" {
			return fmt.Errorf("invalid generated path %q", path)
		}
		output := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(output, pages[path], 0o644); err != nil {
			return err
		}
	}
	return nil
}

func pagesMatch(root string, pages map[string][]byte) (bool, error) {
	for path, want := range pages {
		got, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, err
		}
		if string(got) != string(want) {
			return false, nil
		}
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative != "index.md" {
			if _, ok := pages[relative]; !ok {
				return fmt.Errorf("unexpected generated page %s", relative)
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return true, nil
}
