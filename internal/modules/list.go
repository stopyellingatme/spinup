package modules

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/stopyellingatme/spinup/internal/project"
)

// List prints module metadata and the current project's enabled state.
func List(dir string, asJSON bool, out io.Writer) error {
	if dir == "" {
		dir = "."
	}
	root, err := project.FindRoot(dir)
	if err != nil {
		return fmt.Errorf("list modules: %w\nTry: run inside a spinup project or pass --dir", err)
	}
	cfg, err := project.Load(root)
	if err != nil {
		return fmt.Errorf("list modules: %w\nTry: correct spinup.toml", err)
	}
	type row struct {
		Module
		Enabled bool `json:"enabled"`
	}
	rows := []row{}
	for _, m := range All() {
		rows = append(rows, row{Module: m, Enabled: Enabled(cfg, m.Name)})
	}
	if asJSON {
		return json.NewEncoder(out).Encode(rows)
	}
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "NAME\tSTATE\tREQUIRES\tDESCRIPTION"); err != nil {
		return err
	}
	for _, r := range rows {
		state := "off"
		if r.Enabled {
			state = "on"
		}
		requires := strings.Join(r.Requires, ",")
		if requires == "" {
			requires = "-"
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", r.Name, state, requires, r.Description); err != nil {
			return err
		}
	}
	return table.Flush()
}
