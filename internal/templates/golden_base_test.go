package templates_test

import (
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

// agentFiles are the four agent guidance files of the base pack (ADR 0035 section 1).
var agentFiles = []string{"AGENTS.md", "AGENTS.project.md", "CLAUDE.md", ".claude/skills/spinup/SKILL.md"}

func TestGoldenBase(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "base")
	answers := map[string]any{"app_name": "todo", "module": "example.com/todo"}
	render := func() (fs.FS, error) {
		return testutil.RenderPack(t, []string{"base"}, answers), nil
	}
	fsys, _ := render()
	testutil.Golden(t, "base", fsys)
	testutil.Deterministic(t, render)
	data, err := fs.ReadFile(fsys, "spinup.toml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := project.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.App.Name != "todo" || cfg.App.Module != "example.com/todo" ||
		cfg.BehaviorVersion != 1 || cfg.Monitoring.Profile != "lite" ||
		!reflect.DeepEqual(cfg.Modules, []string{"ci", "deploy"}) {
		t.Fatalf("unexpected project configuration: %+v", cfg)
	}
}

func TestGoldenBaseAgentGuidance(t *testing.T) {
	t.Parallel()
	answers := map[string]any{"app_name": "todo", "module": "example.com/todo", "agent_guidance": true}
	render := func() (fs.FS, error) {
		return testutil.RenderPack(t, []string{"base"}, answers), nil
	}
	fsys, _ := render()
	testutil.Deterministic(t, render)
	for _, name := range agentFiles {
		if _, err := fs.Stat(fsys, name); err != nil {
			t.Errorf("agent guidance file %s is missing: %v", name, err)
		}
	}
	agents, err := fs.ReadFile(fsys, "AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(agents)
	if strings.Contains(text, "[[") {
		t.Errorf("AGENTS.md has a raw template delimiter:\n%s", text)
	}
	if !strings.HasSuffix(text, "\n") {
		t.Error("AGENTS.md does not end in a newline")
	}
	baseRows := []string{
		"| Run the app in development. It runs until you stop it. | `spinup dev` |",
		"| Run the API tests | `spinup test` |",
		"| Find edits of managed files | `spinup check` |",
		"| See how a file differs from its template | `spinup diff <path>` |",
		"| Undo your edit of a managed file. For a file with `.gen.` or `_gen.` in its name, run `git checkout -- <path>`. | `spinup restore <path> --force` |",
		"| Keep your edit of a managed file | `spinup eject <path>` |",
		"| List the modules, or add one | `spinup module list`, then `spinup module add <name>` |",
		"| Check the health of the app | `spinup doctor --json` |",
		"| Upgrade the templates | `spinup upgrade --plan`, then `spinup upgrade` |",
	}
	for _, row := range baseRows {
		if strings.Count(text, row) != 1 {
			t.Errorf("AGENTS.md does not have the base row %q once", row)
		}
	}
	if !strings.Contains(text, "Skip this step. This app has no web app.") {
		t.Error("AGENTS.md of a render without the web pack does not skip the web step")
	}
	claude, err := fs.ReadFile(fsys, "CLAUDE.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(claude) != "@AGENTS.md\n@AGENTS.project.md\n" {
		t.Errorf("CLAUDE.md = %q", claude)
	}
}

func TestGoldenBaseNoAgentGuidance(t *testing.T) {
	t.Parallel()
	answers := map[string]any{"app_name": "todo", "module": "example.com/todo", "agent_guidance": false}
	render := func() (fs.FS, error) {
		return testutil.RenderPack(t, []string{"base"}, answers), nil
	}
	fsys, _ := render()
	testutil.Deterministic(t, render)
	for _, name := range agentFiles {
		if _, err := fs.Stat(fsys, name); err == nil {
			t.Errorf("agent guidance file %s exists with agent_guidance false", name)
		}
	}
	if _, err := fs.Stat(fsys, ".claude"); err == nil {
		t.Error("the directory .claude exists with agent_guidance false")
	}
	if _, err := fs.Stat(fsys, "spinup.toml"); err != nil {
		t.Errorf("spinup.toml is missing: %v", err)
	}
}
