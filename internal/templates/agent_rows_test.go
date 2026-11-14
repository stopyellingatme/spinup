package templates_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/render"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/testutil"
	"github.com/stopyellingatme/spinup/internal/webtool"
)

var agentRowPacks = []string{
	"base", "api", "ci", "deploy", "backups", "jobs", "mail", "monitoring", "secrets", "tofu", "web", "auth", "apikeys", "audit", "billing", "flags", "live", "mcp",
}

var agentRowRead = regexp.MustCompile(`^\[[^\]]+\]\(https://raw\.githubusercontent\.com/stopyellingatme/spinup/main/docs/([a-z0-9/_.-]+\.(?:md|txt))\)$`)

func TestAgentRowsFilesExist(t *testing.T) {
	t.Parallel()
	names, err := templates.List(templates.Embedded())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if strings.HasPrefix(name, "_") {
			continue
		}
		pack, err := templates.Load(templates.Embedded(), name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fs.Stat(pack.FS, "fragments/agents-rows.md.tmpl"); err != nil {
			t.Errorf("pack %q has no agents rows fragment: %v", name, err)
		}
	}
}

func TestAgentRows(t *testing.T) {
	t.Parallel()
	fsys := renderAgentRows(t, agentRowPacks)
	rows := agentRows(t, fsys)
	wantRows := fragmentRowCount(t, agentRowPacks)
	if len(rows) != wantRows {
		t.Errorf("AGENTS.md has %d rows, want %d", len(rows), wantRows)
	}
	for _, row := range rows {
		cells := strings.Split(strings.TrimSuffix(strings.TrimPrefix(row, "| "), " |"), " | ")
		if len(cells) != 3 {
			t.Errorf("row has %d cells: %q", len(cells), row)
			continue
		}
		for _, cell := range cells {
			if strings.TrimSpace(cell) == "" {
				t.Errorf("row has an empty cell: %q", row)
			}
		}
		match := agentRowRead.FindStringSubmatch(cells[2])
		if match == nil {
			t.Errorf("row has an invalid Read cell: %q", row)
			continue
		}
		if _, err := os.Stat(filepath.Join(testutil.RepoRoot(t), "docs", filepath.FromSlash(match[1]))); err != nil {
			t.Errorf("row links to missing docs file %q: %v", match[1], err)
		}
		if !strings.Contains(cells[1], "`") {
			t.Errorf("row has no command span: %q", row)
		}
	}
	assertAgentRowOrder(t, strings.Join(rows, "\n"))

	reversed := append([]string(nil), agentRowPacks...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	other := renderAgentRows(t, reversed)
	got, err := fs.ReadFile(fsys, "AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	want, err := fs.ReadFile(other, "AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("AGENTS.md changes when the pack order changes")
	}

	apiOnly := renderAgentRows(t, []string{"base", "api"})
	apiRows := strings.Join(agentRows(t, apiOnly), "\n")
	if strings.Contains(apiRows, "spinup g live") {
		t.Error("AGENTS.md includes live rows without the live pack")
	}
	if !strings.Contains(apiRows, "spinup g resource <name> <field:type>...") {
		t.Error("AGENTS.md does not include API rows")
	}

	bun := testutil.RenderPack(t, []string{"base", "web"}, agentRowAnswers("bun"))
	bunRows := strings.Join(agentRows(t, bun), "\n")
	if !strings.Contains(bunRows, "bun run check") {
		t.Error("web rows do not use bun")
	}
	pnpm := testutil.RenderPack(t, []string{"base", "web"}, agentRowAnswers(""))
	pnpmRows := strings.Join(agentRows(t, pnpm), "\n")
	if !strings.Contains(pnpmRows, "pnpm run check") {
		t.Error("web rows do not use pnpm by default")
	}
}

func assertAgentRowOrder(t *testing.T, rows string) {
	t.Helper()
	firstRows := []string{
		"Run the app in development. It runs until you stop it.",
		"Add a table with a CRUD API",
		"Add a page",
		"Change the plans or quotas of API keys.",
		"Record who changed a table",
		"Limit a resource to roles",
		"List the backups",
		"Change the plans or prices.",
		"Rebuild the CI workflows",
		"See what a deploy would change",
		"Add or change a feature flag.",
		"Add a background job",
		"Add live updates to a resource that exists",
		"Add an email template and its sender",
		"Add a typed MCP tool",
		"Add a Grafana dashboard",
		"See which secrets changed",
		"Preview the infrastructure changes",
	}
	previous := -1
	for _, first := range firstRows {
		position := strings.Index(rows, first)
		if position < 0 {
			t.Errorf("AGENTS.md has no first row %q", first)
			continue
		}
		if position <= previous {
			t.Errorf("row block %q is out of order", first)
		}
		previous = position
	}
}

func renderAgentRows(t *testing.T, packs []string) fs.FS {
	t.Helper()
	return testutil.RenderPack(t, packs, agentRowAnswers(""))
}

func agentRowAnswers(packageManager string) map[string]any {
	answers := map[string]any{
		"app_name": "todo",
		"module":   "example.com/todo",
		"modules":  "apikeys,audit,auth,backups,billing,ci,deploy,flags,jobs,live,mail,mcp,monitoring,secrets,tofu",
	}
	if packageManager != "" {
		answers["package_manager"] = packageManager
	}
	return answers
}

func agentRows(t *testing.T, fsys fs.FS) []string {
	t.Helper()
	data, err := fs.ReadFile(fsys, "AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	start := -1
	for i, line := range lines {
		if line == "| You need | Run | Read |" && i+1 < len(lines) && lines[i+1] == "| --- | --- | --- |" {
			start = i + 2
			break
		}
	}
	if start < 0 {
		t.Fatal("AGENTS.md has no decision-table header")
	}
	var rows []string
	for _, line := range lines[start:] {
		if line == "## Finish checklist" {
			return rows
		}
		if line != "" {
			rows = append(rows, line)
		}
	}
	t.Fatal("AGENTS.md has no finish checklist")
	return nil
}

func fragmentRowCount(t *testing.T, names []string) int {
	t.Helper()
	count := 0
	for _, name := range names {
		pack, err := templates.Load(templates.Embedded(), name)
		if err != nil {
			t.Fatal(err)
		}
		data, err := fs.ReadFile(pack.FS, "fragments/agents-rows.md.tmpl")
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) != "" {
				count++
			}
		}
	}
	return count
}

var agentSpan = regexp.MustCompile("`([^`]+)`")

// agentSection returns the lines of AGENTS.md from the line that starts with from to the
// line before the next line that starts with to.
func agentSection(t *testing.T, text, from, to string) []string {
	t.Helper()
	var lines []string
	inside := false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, from):
			inside = true
		case inside && strings.HasPrefix(line, to):
			return lines
		}
		if inside {
			lines = append(lines, line)
		}
	}
	t.Fatalf("AGENTS.md has no section from %q to %q", from, to)
	return nil
}

// agentCommandPath returns the words of a spinup span before its first flag or placeholder.
func agentCommandPath(span string) string {
	var words []string
	for _, word := range strings.Fields(span) {
		if strings.HasPrefix(word, "-") || strings.HasPrefix(word, "<") || strings.HasPrefix(word, "[") {
			break
		}
		words = append(words, word)
	}
	return strings.Join(words, " ")
}

// TestAgentGuidanceOwnerCommands proves the review fixes of rules 6 and 7 and of ADR 0035
// section 8.3: a row that runs a command of rule 7 says "Ask the owner first".
func TestAgentGuidanceOwnerCommands(t *testing.T) {
	t.Parallel()
	data, err := fs.ReadFile(renderAgentRows(t, agentRowPacks), "AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	owner := map[string]bool{}
	for _, line := range agentSection(t, text, "7. ", "8. ") {
		for _, match := range agentSpan.FindAllStringSubmatch(line, -1) {
			if strings.HasPrefix(match[1], "spinup ") {
				owner[agentCommandPath(match[1])] = true
			}
		}
	}
	for _, want := range []string{
		"spinup deploy", "spinup deploy unlock", "spinup deploy digital-ocean", "spinup rollback", "spinup releases prune",
		"spinup infra apply", "spinup infra destroy", "spinup server bootstrap", "spinup server ssh", "spinup maintenance",
		"spinup monitor silence", "spinup monitor test-alert",
		"spinup backup now", "spinup backup prune", "spinup backup restore", "spinup backup schedule", "spinup backup verify",
		"spinup secrets push", "spinup secrets rotate", "spinup secrets rekey",
		"spinup db migrate down", "spinup db to", "spinup db redo", "spinup db reset", "spinup db restore",
	} {
		if !owner[want] {
			t.Errorf("rule 7 does not name %q", want)
		}
	}
	if rule6 := agentSection(t, text, "6. ", "7. "); !strings.Contains(strings.Join(rule6, "\n"), "`spinup secrets get`") {
		t.Error("rule 6 does not forbid spinup secrets get")
	}
	for _, row := range agentRows(t, renderAgentRows(t, agentRowPacks)) {
		cells := strings.Split(strings.TrimSuffix(strings.TrimPrefix(row, "| "), " |"), " | ")
		for _, match := range agentSpan.FindAllStringSubmatch(cells[1], -1) {
			span := match[1]
			if !strings.HasPrefix(span, "spinup ") || strings.Contains(span, "--plan") {
				continue
			}
			if owner[agentCommandPath(span)] && !strings.Contains(cells[0], "Ask the owner first.") {
				t.Errorf("row runs %q of rule 7 but does not say \"Ask the owner first.\": %q", span, row)
			}
		}
		if strings.Contains(cells[1], "apikeys create") && !strings.Contains(cells[0], "The owner runs this command, because it prints the secret token.") {
			t.Errorf("the API key row lets the agent see the token: %q", row)
		}
		if strings.Contains(cells[1], "flags enable") && !strings.HasPrefix(cells[0], "Turn a flag on ") {
			t.Errorf("the enable command is in a row that does not turn a flag on: %q", row)
		}
		if strings.Contains(cells[1], "flags disable") && !strings.HasPrefix(cells[0], "Turn a flag off ") {
			t.Errorf("the disable command is in a row that does not turn a flag off: %q", row)
		}
		if strings.Contains(cells[1], "spinup restore") && !strings.Contains(cells[1], "--force") {
			t.Errorf("the restore row omits --force: %q", row)
		}
	}
	for _, want := range []string{"flags enable <key>", "flags disable <key>"} {
		if strings.Count(text, want) != 1 {
			t.Errorf("AGENTS.md names %q %d times, want 1", want, strings.Count(text, want))
		}
	}
}

// TestAgentGuidanceRestoreAdvice proves that AGENTS.md never sends a generated file to
// spinup restore, which writes the pack version and drops the generated registrations.
func TestAgentGuidanceRestoreAdvice(t *testing.T) {
	t.Parallel()
	data, err := fs.ReadFile(renderAgentRows(t, agentRowPacks), "AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, match := range agentSpan.FindAllStringSubmatch(text, -1) {
		if strings.HasPrefix(match[1], "spinup restore ") && match[1] != "spinup restore <path> --force" {
			t.Errorf("AGENTS.md has the restore command %q, want only \"spinup restore <path> --force\"", match[1])
		}
	}
	output := strings.Join(agentSection(t, text, "## Tool output", "## Decision table"), "\n")
	checklist := strings.Join(agentSection(t, text, "4. Run `spinup check`.", "5. "), "\n")
	for name, section := range map[string]string{"Tool output": output, "finish step 4": checklist} {
		if !strings.Contains(section, "`git checkout -- <path>`") {
			t.Errorf("%s does not name git checkout for a generated file", name)
		}
	}
	if !strings.Contains(output, "Do not run `spinup restore` on such a file.") {
		t.Error("the section Tool output does not forbid spinup restore for a generated file")
	}
}

// TestAgentGuidanceToolOutput proves that the table "Tool output" names the real output of
// each tool for both client variants, and no file that the owner can edit.
func TestAgentGuidanceToolOutput(t *testing.T) {
	t.Parallel()
	for _, client := range []string{"openapi-fetch", "heyapi"} {
		t.Run(client, func(t *testing.T) {
			t.Parallel()
			names := []string{"base", "api", "web"}
			loaded := make([]*templates.Pack, 0, len(names))
			for _, name := range names {
				pack, err := templates.Load(templates.Embedded(), name)
				if err != nil {
					t.Fatal(err)
				}
				loaded = append(loaded, pack)
			}
			answers, err := render.ValidateAnswers(loaded, map[string]any{"app_name": "todo", "module": "example.com/todo", "api_client": client})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := render.Render(render.Input{Packs: loaded, Answers: answers, Stack: stackversions.MustLoad(), TemplateVersion: "0.0.0-test"})
			if err != nil {
				t.Fatal(err)
			}
			agents, ok := plan.Get("AGENTS.md")
			if !ok {
				t.Fatal("AGENTS.md is missing")
			}
			var outputs []string
			for _, line := range agentSection(t, string(agents.Data), "## Tool output", "## Decision table") {
				if !strings.HasPrefix(line, "| `") {
					continue
				}
				cell := strings.SplitN(strings.TrimPrefix(line, "| "), " | ", 2)[0]
				for _, match := range agentSpan.FindAllStringSubmatch(cell, -1) {
					outputs = append(outputs, match[1])
				}
			}
			listed := func(path string) bool {
				for _, output := range outputs {
					if path == output || (strings.HasSuffix(output, "/") && strings.HasPrefix(path, output)) {
						return true
					}
				}
				return false
			}
			for _, file := range plan.Files {
				if listed(file.Path) && (file.Class == "managed" || file.Class == "scaffold") {
					t.Errorf("tool output %s is a %s pack file", file.Path, file.Class)
				}
			}
			var want []string
			oapi, ok := plan.Get("api/oapi-codegen.yaml")
			if !ok {
				t.Fatal("api/oapi-codegen.yaml is missing")
			}
			for _, line := range strings.Split(string(oapi.Data), "\n") {
				if value, found := strings.CutPrefix(line, "output: "); found {
					want = append(want, "api/"+strings.TrimSpace(value))
				}
			}
			sqlc, ok := plan.Get("api/sqlc.yaml")
			if !ok {
				t.Fatal("api/sqlc.yaml is missing")
			}
			for _, line := range strings.Split(string(sqlc.Data), "\n") {
				if value, found := strings.CutPrefix(strings.TrimSpace(line), "out: "); found {
					want = append(want, "api/"+strings.TrimSpace(value)+"/x.go")
				}
			}
			tool := webtool.For(answers)
			client := "web/" + tool.ClientOutput()
			if tool.HeyAPI() {
				client += "/x.ts"
			}
			want = append(want, "api/openapi/bundle.gen.yaml", "web/src/lib/api/schema.d.ts", client)
			if len(want) != 5 {
				t.Fatalf("tool outputs = %q", want)
			}
			for _, path := range want {
				if !listed(path) {
					t.Errorf("the table Tool output does not cover %s (outputs %q)", path, outputs)
				}
			}
			for _, path := range []string{"web/src/lib/api/client.ts", "api/internal/gen/generate.go"} {
				if listed(path) {
					t.Errorf("the table Tool output lists the editable file %s", path)
				}
			}
		})
	}
}
