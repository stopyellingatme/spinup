package templates_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestEmbedCaddy(t *testing.T) {
	const live = "\n\thandle /api/live/* {\n\t\timport upstream.caddy api 8080 { flush_interval -1 }\n\t}\n"
	for _, tc := range []struct {
		name, mode, modules, golden string
		live                        bool
	}{
		{"embed", "embed", "", "web-embed", false},
		{"embed-auth", "embed", "auth", "web-embed", false},
		{"embed-mail", "embed", "mail", "web-embed", false},
		{"embed-live", "embed", "auth,live", "web-embed", true},
		// The reset link holds the token in the fragment, so the Caddyfile has no reset block.
		{"embed-reset", "embed", "auth,mail", "web-embed", false},
		{"embed-all", "embed", "auth,mail,live", "web-embed", true},
		{"node-all", "node", "auth,mail,live", "live", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys := testutil.RenderPack(t, []string{"base", "api", "web", "deploy"}, map[string]any{
				"app_name": "todo", "module": "example.com/todo", "frontend_mode": tc.mode, "modules": tc.modules,
			})
			got := readDeployFile(t, fsys, "deploy/Caddyfile")
			if tc.live && strings.Index(got, "handle /api/live/*") >= strings.Index(got, "\n\thandle {") {
				t.Error("live route must precede the catch-all")
			}
			if strings.Contains(got, "resetDocument") {
				t.Error("the Caddyfile has a reset document block")
			}
			if tc.mode != "node" {
				count := strings.Count(got, live)
				if count != boolCount(tc.live) {
					t.Errorf("block count = %d, want %d: %s", count, boolCount(tc.live), live)
				}
				if tc.live {
					got = strings.Replace(got, live, "", 1)
				}
			}
			want, err := os.ReadFile("../../testdata/golden/" + tc.golden + "/deploy/Caddyfile")
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("Caddyfile differs from %s baseline after removing expected blocks", tc.golden)
			}
			if tc.name == "embed-all" {
				t.Run("caddy-adapt", func(t *testing.T) { liveCaddyAdapt(t, writeDeployTree(t, fsys)) })
			}
		})
	}
}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}
