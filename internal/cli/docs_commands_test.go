package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/stopyellingatme/spinup/internal/doctest"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// TestEveryCommandIsDocumented is the "all doc commands tested" gate of the v1.0 release.
// It lives in internal/cli because only this package can build the real command tree
// (newRoot) with every registered command, and the doctest package must not import cli.
// The test checks three rules. Each visible command, group or leaf, appears as
// `spinup <path>` in a page of docs/commands. Each page there has a runnable console block,
// which `make doctest` runs. Each leaf flag appears on its command page, and each table flag
// names a CLI flag.
func TestEveryCommandIsDocumented(t *testing.T) {
	var entered bool
	app := &App{Flags: &GlobalFlags{}, UI: ui.New(nil, &bytes.Buffer{}, &bytes.Buffer{}, ui.Options{})}
	root := newRoot(app, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, &entered)

	dir := filepath.Join(repoRoot(t), "docs", "commands")
	pages, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil || len(pages) == 0 {
		t.Fatalf("no pages in %s: %v", dir, err)
	}
	var corpus strings.Builder
	documentation := make([]commandDocumentation, 0, len(pages))
	for _, page := range pages {
		data, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		corpus.Write(data)
		corpus.WriteByte('\n')
		documentation = append(documentation, commandDocumentation{
			path:  page,
			text:  string(data),
			flags: documentedTableFlags(string(data)),
		})
		parsed, err := doctest.Parse(page, data)
		if err != nil {
			t.Errorf("%s: %v", page, err)
			continue
		}
		runnable := 0
		for _, block := range parsed.Blocks {
			if !block.Skip {
				runnable++
			}
		}
		if runnable == 0 {
			t.Errorf("%s has no runnable doctest block. Try: add a console block that make doctest can run", page)
		}
	}
	globalFlags := flagNames(root.PersistentFlags())
	knownGeneratorFlags := generatorFlagNames(root)
	checkDocumentedFlags(t, root, documentation, corpus.String(), globalFlags, knownGeneratorFlags)

	var missing []string
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, child := range cmd.Commands() {
			if child.Hidden || child.Name() == "help" || child.Name() == "completion" {
				continue
			}
			documented := false
			for _, path := range commandPaths(child) {
				if regexp.MustCompile(`spinup ` + regexp.QuoteMeta(path) + `\b`).MatchString(corpus.String()) {
					documented = true
					break
				}
			}
			if !documented {
				missing = append(missing, child.CommandPath())
			}
			walk(child)
		}
	}
	walk(root)
	sort.Strings(missing)
	for _, path := range missing {
		t.Errorf("command %q is not documented in docs/commands. Try: mention `%s` in a page there", path, path)
	}
}

type commandDocumentation struct {
	path  string
	text  string
	flags map[string]bool
}

var documentedFlag = regexp.MustCompile("(--[a-z0-9][a-z0-9-]*)")

// documentedFlagExceptions lists table values that document Cobra help, not a command flag.
// Cobra adds --help to every command after the command builders run.
var documentedFlagExceptions = map[string]string{
	"clean":     "The db restore page documents the pg_restore flag.",
	"help":      "Cobra adds this flag to every command.",
	"if-exists": "The db restore page documents the pg_restore flag.",
}

func documentedTableFlags(text string) map[string]bool {
	flags := make(map[string]bool)
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		for _, match := range documentedFlag.FindAllStringSubmatch(line, -1) {
			flags[strings.TrimPrefix(match[1], "--")] = true
		}
	}
	return flags
}

func checkDocumentedFlags(t *testing.T, root *cobra.Command, pages []commandDocumentation, corpus string, globalFlags, knownGeneratorFlags map[string]bool) {
	t.Helper()
	for name := range globalFlags {
		flag := "--" + name
		if !strings.Contains(corpus, flag) {
			t.Errorf("global flag %q is not documented. Try: add it to a docs/commands flag table", flag)
		}
	}
	for _, cmd := range leafCommands(root) {
		commandPages := pagesForCommand(cmd, pages)
		for name := range commandFlagNames(cmd) {
			if name == "help" {
				continue
			}
			flag := "--" + name
			if globalFlags[name] {
				continue
			}
			if !commandPagesContain(commandPages, flag) {
				t.Errorf("flag %q for %q is not documented on its command page. Try: add it to the page that documents %s", flag, cmd.CommandPath(), cmd.CommandPath())
			}
		}
	}

	valid := make(map[string]bool)
	for _, cmd := range leafCommands(root) {
		for name := range commandFlagNames(cmd) {
			valid[name] = true
		}
	}
	for name := range globalFlags {
		valid[name] = true
	}
	for name := range knownGeneratorFlags {
		valid[name] = true
	}
	for _, page := range pages {
		for name := range page.flags {
			if valid[name] || documentedFlagExceptions[name] != "" {
				continue
			}
			t.Errorf("%s documents unknown flag %q. Try: correct the flag table or add a short exception", page.path, "--"+name)
		}
	}
}

func leafCommands(root *cobra.Command) []*cobra.Command {
	var leaves []*cobra.Command
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		children := 0
		for _, child := range cmd.Commands() {
			if child.Hidden || child.Name() == "help" || child.Name() == "completion" {
				continue
			}
			children++
			walk(child)
		}
		if children != 0 || cmd == root {
			return
		}
		leaves = append(leaves, cmd)
	}
	walk(root)
	return leaves
}

func flagNames(flags *pflag.FlagSet) map[string]bool {
	names := make(map[string]bool)
	flags.VisitAll(func(flag *pflag.Flag) {
		names[flag.Name] = true
	})
	return names
}

func commandFlagNames(cmd *cobra.Command) map[string]bool {
	names := make(map[string]bool)
	for current := cmd; current != nil; current = current.Parent() {
		for name := range flagNames(current.LocalFlags()) {
			names[name] = true
		}
		for name := range flagNames(current.PersistentFlags()) {
			names[name] = true
		}
	}
	return names
}

func generatorFlagNames(root *cobra.Command) map[string]bool {
	names := make(map[string]bool)
	for _, cmd := range root.Commands() {
		if cmd.Name() != "generate" {
			continue
		}
		for _, generator := range cmd.Commands() {
			for name := range flagNames(generator.LocalFlags()) {
				names[name] = true
			}
		}
	}
	return names
}

func pagesForCommand(cmd *cobra.Command, pages []commandDocumentation) []commandDocumentation {
	var matched []commandDocumentation
	for _, page := range pages {
		for current := cmd; current != nil; current = current.Parent() {
			for _, path := range commandPaths(current) {
				if regexp.MustCompile(`spinup ` + regexp.QuoteMeta(path) + `\b`).MatchString(page.text) {
					matched = append(matched, page)
					break
				}
			}
		}
	}
	return matched
}

func commandPagesContain(pages []commandDocumentation, flag string) bool {
	for _, page := range pages {
		if strings.Contains(page.text, flag) {
			return true
		}
	}
	return false
}

// commandPaths lists the paths of cmd below the root, with each alias of each ancestor.
// A page may write `spinup g resource` for `spinup generate resource`.
func commandPaths(cmd *cobra.Command) []string {
	if !cmd.HasParent() {
		return []string{""}
	}
	var out []string
	for _, prefix := range commandPaths(cmd.Parent()) {
		for _, name := range append([]string{cmd.Name()}, cmd.Aliases...) {
			out = append(out, strings.TrimSpace(prefix+" "+name))
		}
	}
	return out
}
