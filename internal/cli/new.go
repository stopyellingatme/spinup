package cli

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen/newapp"
)

func init() { Register(GroupDevelop, newNew) }

func newNew(app *App) *cobra.Command {
	var o newapp.Options
	packs, loadErr := newapp.Catalog()
	questions, questionErr := newapp.Questions(packs)
	c := &cobra.Command{Use: "new <name>", Short: "Create an application from template packs", Args: cobra.ExactArgs(1)}
	c.Flags().BoolVar(&o.APIOnly, "api-only", false, "create only the Go API")
	c.Flags().BoolVar(&o.NoGit, "no-git", false, "do not initialize a git repository")
	for _, q := range questions {
		if q.Key == "app_name" {
			continue
		}
		name := strings.ReplaceAll(q.Key, "_", "-")
		if q.Type == "bool" {
			c.Flags().Bool(name, q.Default == "true", q.Prompt)
		} else {
			c.Flags().String(name, q.Default, q.Prompt)
		}
	}
	c.RunE = func(c *cobra.Command, args []string) error {
		if loadErr != nil {
			return loadErr
		}
		if questionErr != nil {
			return questionErr
		}
		o.Name = args[0]
		o.Dir = app.Flags.Dir
		o.Version = Version
		o.KitReplace = os.Getenv("SPINUP_KIT_REPLACE")
		o.Interactive = app.UI.Interactive()
		o.DryRun = app.Flags.DryRun
		o.JSON = app.Flags.JSON
		o.Answers = map[string]any{}
		for _, q := range questions {
			name := strings.ReplaceAll(q.Key, "_", "-")
			if q.Key == "app_name" || !c.Flags().Changed(name) {
				continue
			}
			if q.Type == "bool" {
				value, err := c.Flags().GetBool(name)
				if err != nil {
					return err
				}
				o.Answers[q.Key] = value
			} else {
				value, err := c.Flags().GetString(name)
				if err != nil {
					return err
				}
				o.Answers[q.Key] = value
			}
		}
		result, err := newapp.Generate(c.Context(), o, newapp.HuhPrompter{In: c.InOrStdin(), Out: c.ErrOrStderr()})
		if err != nil {
			return err
		}
		return newapp.Apply(c.Context(), result, o, exec.OSRunner{}, app.UI)
	}
	return c
}
