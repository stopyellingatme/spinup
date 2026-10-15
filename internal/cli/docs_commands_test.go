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

	"github.com/stopyellingatme/spinup/internal/doctest"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// TestEveryCommandIsDocumented is the "all doc commands tested" gate of the v1.0 release.
// It lives in internal/cli because only this package can build the real command tree
// (newRoot) with every registered command, and the doctest package must not import cli.
// The test checks two rules. Each visible command, group or leaf, appears as
// `spinup <path>` in a page of docs/commands. Each page there has a runnable console block,
// which `make doctest` runs.
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
	for _, page := range pages {
		data, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		corpus.Write(data)
		corpus.WriteByte('\n')
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
