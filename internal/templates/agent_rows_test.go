package templates_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/testutil"
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
