package migrate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/migrate"
	"github.com/stopyellingatme/spinup/internal/migrate/migratetest"
)

const (
	ownAgents  = "# Notes\nUse tabs.\n"
	ownClaude  = "# Mine\nBe brief.\n"
	ownProject = "# Project\nShip on Fridays.\n"
	imports    = "@AGENTS.md\n@AGENTS.project.md\n"
)

func TestCodemodAgentGuidanceRegistered(t *testing.T) {
	t.Parallel()
	m := kitMigration(t, migrate.AgentGuidanceID)
	if m.ID != "0005_agent_guidance" || m.Since != "0.1.0-alpha.783" || m.Stage != migrate.Before {
		t.Fatalf("migration = %+v", m)
	}
	if m.Desc != "Keep the notes of an app that has its own AGENTS.md and CLAUDE.md" {
		t.Fatalf("desc = %q", m.Desc)
	}
	pending, err := migrate.Pending("0.1.0-alpha.782", "0.1.0-alpha.783", nil, migrate.Before)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range pending {
		found = found || p.ID == migrate.AgentGuidanceID
	}
	if !found {
		t.Fatal("the codemod is not pending from 0.1.0-alpha.782 to 0.1.0-alpha.783")
	}
}

// TestCodemodAgentGuidance covers each row of ADR 0035 section 3.4. migratetest.Run also
// proves that Plan writes nothing and that a second Apply changes nothing.
func TestCodemodAgentGuidance(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		before, after map[string]string
	}{
		{
			name:   "move AGENTS.md",
			before: map[string]string{"AGENTS.md": ownAgents},
			after:  map[string]string{"AGENTS.project.md": ownAgents},
		},
		{
			name:   "append to AGENTS.project.md",
			before: map[string]string{"AGENTS.md": ownAgents, "AGENTS.project.md": ownProject},
			after:  map[string]string{"AGENTS.project.md": ownProject + "\n## Notes from the former AGENTS.md\n\n" + ownAgents},
		},
		{
			name:   "append without final newlines",
			before: map[string]string{"AGENTS.md": "Use tabs.", "AGENTS.project.md": "# Project"},
			after:  map[string]string{"AGENTS.project.md": "# Project\n\n## Notes from the former AGENTS.md\n\nUse tabs.\n"},
		},
		{
			name:   "move and import",
			before: map[string]string{"AGENTS.md": ownAgents, "CLAUDE.md": ownClaude},
			after:  map[string]string{"AGENTS.project.md": ownAgents, "CLAUDE.md": ownClaude + "\n" + imports},
		},
		{
			name:   "append and import",
			before: map[string]string{"AGENTS.md": ownAgents, "AGENTS.project.md": ownProject, "CLAUDE.md": ownClaude},
			after: map[string]string{
				"AGENTS.project.md": ownProject + "\n## Notes from the former AGENTS.md\n\n" + ownAgents,
				"CLAUDE.md":         ownClaude + "\n" + imports,
			},
		},
		{
			name:   "CLAUDE.md with no final newline",
			before: map[string]string{"AGENTS.md": ownAgents, "CLAUDE.md": "# Mine\nBe brief."},
			after:  map[string]string{"AGENTS.project.md": ownAgents, "CLAUDE.md": ownClaude + "\n" + imports},
		},
		{
			name:   "CLAUDE.md has one import line",
			before: map[string]string{"AGENTS.md": ownAgents, "CLAUDE.md": "# Mine\n  @AGENTS.md  \n"},
			after:  map[string]string{"AGENTS.project.md": ownAgents, "CLAUDE.md": "# Mine\n  @AGENTS.md  \n\n@AGENTS.project.md\n"},
		},
		{
			name:   "CLAUDE.md has both import lines",
			before: map[string]string{"AGENTS.md": ownAgents, "CLAUDE.md": "@AGENTS.project.md\n@AGENTS.md"},
			after:  map[string]string{"AGENTS.project.md": ownAgents, "CLAUDE.md": "@AGENTS.project.md\n@AGENTS.md"},
		},
		{
			name:   "empty CLAUDE.md",
			before: map[string]string{"AGENTS.md": ownAgents, "CLAUDE.md": ""},
			after:  map[string]string{"AGENTS.project.md": ownAgents, "CLAUDE.md": imports},
		},
		{
			name:   "CLAUDE.md only",
			before: map[string]string{"CLAUDE.md": ownClaude},
			after:  map[string]string{"CLAUDE.md": ownClaude},
		},
		{
			name:   "no AGENTS.md",
			before: map[string]string{"AGENTS.project.md": ownProject},
			after:  map[string]string{"AGENTS.project.md": ownProject},
		},
		{
			name:   "a run that stopped after the move",
			before: map[string]string{"AGENTS.project.md": ownAgents, "CLAUDE.md": ownClaude},
			after:  map[string]string{"AGENTS.project.md": ownAgents, "CLAUDE.md": ownClaude + "\n" + imports},
		},
		{
			name:   "no files",
			before: map[string]string{"README.md": "# demo\n"},
			after:  map[string]string{"README.md": "# demo\n"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := kitMigration(t, migrate.AgentGuidanceID)
			migratetest.Run(t, m, writeTree(t, tc.before), writeTree(t, tc.after))
		})
	}
}

func TestCodemodAgentGuidancePlan(t *testing.T) {
	t.Parallel()
	m := kitMigration(t, migrate.AgentGuidanceID)
	root := writeTree(t, map[string]string{"AGENTS.md": ownAgents, "CLAUDE.md": ownClaude})
	c := &migrate.Ctx{Root: root, Answers: map[string]any{}, Lock: &manifest.Lock{}}
	changes, err := m.Plan(c)
	if err != nil {
		t.Fatal(err)
	}
	want := []migrate.Change{
		{Path: "AGENTS.project.md", Desc: "Move AGENTS.md to AGENTS.project.md"},
		{Path: "CLAUDE.md", Desc: "Import AGENTS.md and AGENTS.project.md in CLAUDE.md"},
	}
	if len(changes) != len(want) {
		t.Fatalf("changes = %+v", changes)
	}
	for i := range want {
		if changes[i] != want[i] {
			t.Errorf("change %d = %+v, want %+v", i, changes[i], want[i])
		}
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.project.md")); !os.IsNotExist(err) {
		t.Fatalf("Plan wrote AGENTS.project.md: %v", err)
	}
	both := writeTree(t, map[string]string{"AGENTS.md": ownAgents, "AGENTS.project.md": ownProject})
	changes, err = m.Plan(&migrate.Ctx{Root: both})
	if err != nil || len(changes) != 1 || changes[0].Path != "AGENTS.project.md" {
		t.Fatalf("changes = %+v, error = %v", changes, err)
	}
	if err := m.Apply(c); err != nil {
		t.Fatal(err)
	}
	changes, err = m.Plan(c)
	if err != nil || len(changes) != 0 {
		t.Fatalf("plan after Apply = %+v, error = %v", changes, err)
	}
}

// TestCodemodAgentGuidanceSkips covers the two skip rules of ADR 0035 section 3.3.
func TestCodemodAgentGuidanceSkips(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		ctx  migrate.Ctx
	}{
		{"agent_guidance false", migrate.Ctx{Answers: map[string]any{"agent_guidance": false}}},
		{"AGENTS.md in the lock", migrate.Ctx{Lock: &manifest.Lock{Files: map[string]manifest.FileEntry{"AGENTS.md": {Class: manifest.Managed}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{"AGENTS.md": ownAgents, "CLAUDE.md": ownClaude}
			root := writeTree(t, files)
			m := kitMigration(t, migrate.AgentGuidanceID)
			c := tc.ctx
			c.Root = root
			changes, err := m.Plan(&c)
			if err != nil || len(changes) != 0 {
				t.Fatalf("changes = %+v, error = %v", changes, err)
			}
			if err := m.Apply(&c); err != nil {
				t.Fatal(err)
			}
			for name, want := range files {
				got, err := os.ReadFile(filepath.Join(root, name))
				if err != nil || string(got) != want {
					t.Errorf("%s = %q, %v; want %q", name, got, err, want)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "AGENTS.project.md")); !os.IsNotExist(err) {
				t.Errorf("AGENTS.project.md exists: %v", err)
			}
		})
	}
	// A true answer does not skip.
	root := writeTree(t, map[string]string{"AGENTS.md": ownAgents})
	m := kitMigration(t, migrate.AgentGuidanceID)
	if err := m.Apply(&migrate.Ctx{Root: root, Answers: map[string]any{"agent_guidance": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.project.md")); err != nil {
		t.Fatalf("AGENTS.project.md is missing: %v", err)
	}
}
