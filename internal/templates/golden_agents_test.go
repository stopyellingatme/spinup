package templates_test

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

var agentGoldenFiles = []string{
	"AGENTS.md", "AGENTS.project.md", "CLAUDE.md", ".claude/skills/spinup/SKILL.md",
}

func TestGoldenAgents(t *testing.T) {
	t.Parallel()
	if !testutil.GoldenSelected(t, "agents") {
		return
	}
	render := func() (fs.FS, error) {
		all := renderAgentRows(t, agentRowPacks)
		return agentFilesOnly(t, all), nil
	}
	fsys, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "agents", fsys)
	testutil.Deterministic(t, render)
}

func agentFilesOnly(t *testing.T, source fs.FS) fs.FS {
	t.Helper()
	files := fstest.MapFS{}
	for _, name := range agentGoldenFiles {
		data, err := fs.ReadFile(source, name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = &fstest.MapFile{Data: data, Mode: 0o644}
	}
	return files
}
