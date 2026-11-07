package resource

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/fields"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func TestMCPResource(t *testing.T) {
	name, err := fields.ParseName("Note")
	if err != nil {
		t.Fatal(err)
	}
	spec := Spec{Root: t.TempDir(), Name: name, Roles: []string{"manager"}}
	write(t, spec.Root, map[string]string{"api/go.mod": "module example.com/demo/api\n"})
	out, err := addMCP(spec, gen.Input{Lock: &manifest.Lock{Packs: []string{"mcp"}}}, &gen.Output{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Plan.Files) != 2 {
		t.Fatalf("files = %d", len(out.Plan.Files))
	}
	source := string(out.Plan.Files[0].Data)
	const golden = "testdata/mcp_tools.golden"
	if os.Getenv("SPINUP_GOLDEN_UPDATE") == "1" {
		if err := os.MkdirAll("testdata", 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(source), want) {
		t.Fatalf("MCP resource source differs from %s", golden)
	}
	for _, want := range []string{
		"// spinup:mcp-tool registerNotesTools", "handlers.NewServer(d.DB)",
		`Name: "list_notes"`, `Name: "get_note"`, `Name: "create_note"`, `Name: "update_note"`, `Name: "delete_note"`,
		"gen.ListNotesRequestObject", "gen.GetNoteRequestObject", "gen.CreateNoteRequestObject", "gen.UpdateNoteRequestObject", "gen.DeleteNoteRequestObject",
		"gen.NoteInput", "limit = 50", "in.Limit > 200", "ReadOnly: true", "Destructive: true", "Idempotent: true", `Roles: []string{"manager"}`,
		"gen.GetNote404ApplicationProblemPlusJSONResponse", "gen.CreateNote422ApplicationProblemPlusJSONResponse", "problem.Problem.Detail",
	} {
		if !strings.Contains(source, want) {
			t.Errorf("missing %q\n%s", want, source)
		}
	}
	if !strings.Contains(string(out.Plan.Files[1].Data), "registerNotesTools(s, d)") {
		t.Fatal("missing registration")
	}
	if out.Plan.Files[0].Class != "scaffold" || out.Plan.Files[1].Class != "managed" {
		t.Fatal("incorrect classes")
	}
}

func TestMCPResourceNeedsPack(t *testing.T) {
	_, err := addMCP(Spec{}, gen.Input{}, &gen.Output{})
	if err == nil || !strings.Contains(err.Error(), "Try: spinup module add mcp") {
		t.Fatalf("%v", err)
	}
}
