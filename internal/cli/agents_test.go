package cli

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/render"
	"github.com/stopyellingatme/spinup/internal/testutil"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// agentGuidancePacks are the 18 built-in packs, in the order of TestGoldenAgents (unit AG3).
var agentGuidancePacks = []string{
	"base", "api", "ci", "deploy", "backups", "jobs", "mail", "monitoring", "secrets", "tofu", "web", "auth", "apikeys", "audit", "billing", "flags", "live", "mcp",
}

// agentGuidanceFiles are the four files of ADR 0035 section 1.
var agentGuidanceFiles = []string{"AGENTS.md", "AGENTS.project.md", "CLAUDE.md", ".claude/skills/spinup/SKILL.md"}

var (
	agentSpan      = regexp.MustCompile("`([^`]+)`")
	agentUsageWord = regexp.MustCompile(`[A-Za-z0-9_-]+`)
	agentDocsURL   = regexp.MustCompile(regexp.QuoteMeta(render.DocsBase) + `[A-Za-z0-9/_.-]*`)
)

func agentGuidanceRoot() *cobra.Command {
	var entered bool
	app := &App{Flags: &GlobalFlags{}, UI: ui.New(nil, &bytes.Buffer{}, &bytes.Buffer{}, ui.Options{})}
	return newRoot(app, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, &entered)
}

// agentGuidanceText renders an app with all packs and returns the text of each guidance file.
func agentGuidanceText(t *testing.T) map[string]string {
	t.Helper()
	fsys := testutil.RenderPack(t, agentGuidancePacks, map[string]any{
		"app_name": "todo",
		"module":   "example.com/todo",
		"modules":  "apikeys,audit,auth,backups,billing,ci,deploy,flags,jobs,live,mail,mcp,monitoring,secrets,tofu",
	})
	texts := make(map[string]string, len(agentGuidanceFiles))
	for _, name := range agentGuidanceFiles {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		texts[name] = string(data)
	}
	return texts
}

// agentCommandSpans returns each backtick span of text that starts with "spinup ".
func agentCommandSpans(text string) []string {
	var spans []string
	for _, match := range agentSpan.FindAllStringSubmatch(text, -1) {
		if strings.HasPrefix(match[1], "spinup ") {
			spans = append(spans, match[1])
		}
	}
	return spans
}

// checkAgentCommands checks each spinup command span of text against the command tree.
// It returns one error for each wrong command, argument, or flag.
func checkAgentCommands(root *cobra.Command, text string) []error {
	var errs []error
	for _, span := range agentCommandSpans(text) {
		parts := strings.Split(strings.ReplaceAll(span, ", and ", ", then "), ", then ")
		for _, part := range parts {
			if err := checkAgentCommand(root, part); err != nil {
				errs = append(errs, fmt.Errorf("span `%s`: %w", span, err))
			}
		}
	}
	return errs
}

func checkAgentCommand(root *cobra.Command, part string) error {
	words := strings.Fields(part)
	if len(words) == 0 || words[0] != "spinup" {
		return fmt.Errorf("%q does not start with spinup", part)
	}
	var kept []string
	for _, word := range words[1:] {
		if strings.HasPrefix(word, "<") || strings.HasPrefix(word, "[") || word == "..." {
			continue
		}
		kept = append(kept, word)
	}
	path := kept
	var rest []string
	for i, word := range kept {
		if strings.HasPrefix(word, "-") {
			path, rest = kept[:i], kept[i:]
			break
		}
	}
	cmd, extra, err := root.Find(path)
	if err != nil {
		return fmt.Errorf("command %q: %w", part, err)
	}
	if cmd == root {
		return fmt.Errorf("command %q names no subcommand", part)
	}
	// A literal argument, such as "up" in "spinup db migrate up", must be a word of the
	// usage line of the command. Other words are a wrong subcommand.
	usage := map[string]bool{}
	for _, word := range agentUsageWord.FindAllString(strings.TrimPrefix(cmd.Use, cmd.Name()), -1) {
		usage[word] = true
	}
	for _, word := range extra {
		if !usage[word] {
			return fmt.Errorf("command %q: %q is not a subcommand or an argument of %q", part, word, cmd.CommandPath())
		}
	}
	for i := 0; i < len(rest); i++ {
		word := rest[i]
		if !strings.HasPrefix(word, "--") {
			return fmt.Errorf("command %q: unexpected word %q", part, word)
		}
		name, _, hasValue := strings.Cut(strings.TrimPrefix(word, "--"), "=")
		if name == "help" {
			continue
		}
		flag := cmd.Flag(name)
		if flag == nil {
			return fmt.Errorf("command %q: %q has no flag --%s", part, cmd.CommandPath(), name)
		}
		// A flag that needs a value takes the next word, unless the value was a placeholder.
		if !hasValue && flag.NoOptDefVal == "" && i+1 < len(rest) && !strings.HasPrefix(rest[i+1], "-") {
			i++
		}
	}
	return nil
}

// TestAgentGuidanceCommandsExist checks the guidance text of ADR 0035 section 9.1. Each
// spinup command and flag exists, each generator has a row, each doctor check ID exists,
// and each docs URL names a file under docs/.
func TestAgentGuidanceCommandsExist(t *testing.T) {
	root := agentGuidanceRoot()
	texts := agentGuidanceText(t)
	spans := 0
	for _, name := range agentGuidanceFiles {
		spans += len(agentCommandSpans(texts[name]))
		for _, err := range checkAgentCommands(root, texts[name]) {
			t.Errorf("%s: %v. Try: correct the row in fragments/agents-rows.md.tmpl or the base file", name, err)
		}
	}
	if spans < 50 {
		t.Fatalf("found only %d spinup command spans. Try: check the render of the guidance files", spans)
	}
	skill := texts[".claude/skills/spinup/SKILL.md"]
	if !strings.Contains(skill, "`AGENTS.md`") {
		t.Error("SKILL.md does not name AGENTS.md")
	}

	agents := texts["AGENTS.md"]
	generate, _, err := root.Find([]string{"generate"})
	if err != nil || generate.Name() != "generate" {
		t.Fatalf("find generate: %v", err)
	}
	kinds := 0
	for _, child := range generate.Commands() {
		if child.Hidden || child.Name() == "help" {
			continue
		}
		kinds++
		if !regexp.MustCompile("`spinup g " + regexp.QuoteMeta(child.Name()) + `[ ` + "`]").MatchString(agents) {
			t.Errorf("AGENTS.md has no row for the generator %q. Try: add a row with `spinup g %s` to the fragment of its pack", child.Name(), child.Name())
		}
	}
	if kinds == 0 {
		t.Fatal("the generate command has no generators")
	}

	checks := map[string]bool{}
	for _, check := range doctor.Checks(doctor.Local, false) {
		checks[check.ID] = true
	}
	for _, name := range agentGuidanceFiles {
		for _, match := range agentSpan.FindAllStringSubmatch(texts[name], -1) {
			id := match[1]
			if strings.Contains(id, " ") {
				continue
			}
			if (strings.HasSuffix(id, "-drift") || strings.HasSuffix(id, "-wired") || strings.HasSuffix(id, "-bundle")) && !checks[id] {
				t.Errorf("%s names the doctor check %q, which does not exist. Try: use an ID of spinup doctor --json", name, id)
			}
		}
	}

	docs := filepath.Join(repoRoot(t), "docs")
	links := 0
	for _, name := range agentGuidanceFiles {
		for _, url := range agentDocsURL.FindAllString(texts[name], -1) {
			links++
			path := strings.TrimSuffix(strings.TrimPrefix(url, render.DocsBase), ".")
			if _, err := os.Stat(filepath.Join(docs, filepath.FromSlash(path))); err != nil {
				t.Errorf("%s links to %s, which is not a file under docs/. Try: correct the docurl path", name, url)
			}
		}
	}
	if !strings.Contains(agents, render.DocsBase+"llms.txt") || links < 2 {
		t.Errorf("AGENTS.md has %d docs links and must link to llms.txt", links)
	}
}

// TestAgentGuidanceCommandsExistFindsWrongNames proves that the check finds a wrong
// command and a wrong flag.
func TestAgentGuidanceCommandsExistFindsWrongNames(t *testing.T) {
	root := agentGuidanceRoot()
	errs := checkAgentCommands(root, "See `spinup nope` and `spinup g resource --bogus`")
	if len(errs) != 2 {
		t.Fatalf("got %d errors, want 2: %v", len(errs), errs)
	}
	for _, text := range []string{
		"`spinup g nope <name>`",
		"`spinup g service <name> --lang rust extra`",
		"`spinup dev --bogus=1`",
		"`spinup upgrade --plan, then spinup nope`",
	} {
		if errs := checkAgentCommands(root, text); len(errs) != 1 {
			t.Errorf("%s: got %d errors, want 1: %v", text, len(errs), errs)
		}
	}
	for _, text := range []string{
		"`spinup g --help`",
		"`spinup g service <name> --lang rust`",
		"`spinup deploy --plan --env <env>`",
		"`spinup g resource <name> <field:type>... --role <roles>`",
		"`spinup upgrade --plan`, then `spinup upgrade`",
		"`spinup module list, then spinup module add <name>`",
	} {
		if errs := checkAgentCommands(root, text); len(errs) != 0 {
			t.Errorf("%s: %v", text, errs)
		}
	}
}
