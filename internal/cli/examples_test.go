package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/ui"
)

// TestEveryCommandHasExample keeps `--help` and the generated CLI reference useful. Each visible
// command that runs has an example. Each example line is a `spinup` command that names a real
// command, and each flag in it is a flag of that command. A comment line starts with "#".
func TestEveryCommandHasExample(t *testing.T) {
	var entered bool
	app := &App{Flags: &GlobalFlags{}, UI: ui.New(nil, &bytes.Buffer{}, &bytes.Buffer{}, ui.Options{})}
	root := newRoot(app, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, &entered)

	var visit func(*cobra.Command)
	visit = func(command *cobra.Command) {
		for _, child := range command.Commands() {
			if child.Hidden || child.Name() == "help" || child.Name() == "completion" {
				continue
			}
			if child.Runnable() && strings.TrimSpace(child.Example) == "" {
				t.Errorf("%s has no Example", child.CommandPath())
			}
			checkExample(t, root, child)
			visit(child)
		}
	}
	visit(root)
}

func checkExample(t *testing.T, root, command *cobra.Command) {
	t.Helper()
	for _, line := range strings.Split(command.Example, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if fields[0] != "spinup" {
			t.Errorf("%s: example line %q does not start with spinup", command.CommandPath(), line)
			continue
		}
		target, rest, err := root.Find(fields[1:])
		if err != nil || target == root {
			t.Errorf("%s: example line %q names no command: %v", command.CommandPath(), line, err)
			continue
		}
		for _, word := range rest {
			if !strings.HasPrefix(word, "-") || word == "-" || word == "--" {
				continue
			}
			name := strings.SplitN(strings.TrimLeft(word, "-"), "=", 2)[0]
			var known bool
			if strings.HasPrefix(word, "--") {
				known = target.Flags().Lookup(name) != nil || target.InheritedFlags().Lookup(name) != nil
			} else {
				known = target.Flags().ShorthandLookup(name[:1]) != nil || target.InheritedFlags().ShorthandLookup(name[:1]) != nil
			}
			if !known {
				t.Errorf("%s: example line %q uses unknown flag %s of %s", command.CommandPath(), line, word, target.CommandPath())
			}
		}
	}
}
