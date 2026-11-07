package upgrade

import (
	"bytes"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/modules"
)

func TestMCPModuleAddNextSteps(t *testing.T) {
	t.Parallel()
	r, _ := moduleFixture(t, false)
	version := r.Source.(chainSource)["0.1.0"]
	fsys := version.Packs.(fstest.MapFS)
	fsys["mcp/pack.toml"] = &fstest.MapFile{Data: []byte("name='mcp'\nrequires=['api']\n[[files]]\npath='api/internal/mcp/server.go'\nsource='server.go'\nclass='scaffold'\n")}
	fsys["mcp/server.go"] = &fstest.MapFile{Data: []byte("package mcp\n")}
	if err := r.AddModules(t.Context(), ModuleOptions{Names: []string{"mcp"}, Options: Options{Yes: true, NoVerify: true}}); err != nil {
		t.Fatal(err)
	}
	out := r.Out.(*bytes.Buffer).String()
	want := strings.ReplaceAll(modules.MCPNextSteps(), "<module>", "example.com/demo")
	if !strings.Contains(out, want) || !strings.Contains(out, "git merge spinup/module-mcp") {
		t.Fatalf("output=%s", out)
	}
}
