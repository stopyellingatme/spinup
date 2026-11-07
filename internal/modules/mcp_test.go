package modules

import (
	"strings"
	"testing"
)

func TestMCPNextSteps(t *testing.T) {
	t.Parallel()
	for _, want := range []string{
		"spinup apikeys create",
		"claude mcp add --transport http",
		"Authorization: Bearer <key>",
		"spinup g mcp tool <name>",
	} {
		if !strings.Contains(MCPNextSteps(), want) {
			t.Errorf("missing %q", want)
		}
	}
}
