package templates_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestEmbedCaddy(t *testing.T) {
	const live = "\n\thandle /api/live/* {\n\t\timport upstream.caddy api 8080 { flush_interval -1 }\n\t}\n"
	const reset = "\n\t# Defer these headers so they replace the site and upstream values.\n\t@resetDocument {\n\t\tmethod GET HEAD\n\t\tpath_regexp resetDocument ^/reset/[^/]+$\n\t}\n\theader @resetDocument {\n\t\tdefer\n\t\tReferrer-Policy no-referrer\n\t\tCache-Control no-store\n\t}\n"
	for _, tc := range []struct {
		name, mode, modules, golden string
		live, reset                 bool
	}{
		{"embed", "embed", "", "web-embed", false, false},
		{"embed-auth", "embed", "auth", "web-embed", false, false},
		{"embed-mail", "embed", "mail", "web-embed", false, false},
		{"embed-live", "embed", "auth,live", "web-embed", true, false},
		{"embed-reset", "embed", "auth,mail", "web-embed", false, true},
		{"embed-all", "embed", "auth,mail,live", "web-embed", true, true},
		{"node-all", "node", "auth,mail,live", "live", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys := testutil.RenderPack(t, []string{"base", "api", "web", "deploy"}, map[string]any{
				"app_name": "todo", "module": "example.com/todo", "frontend_mode": tc.mode, "modules": tc.modules,
			})
			got := readDeployFile(t, fsys, "deploy/Caddyfile")
			if tc.live && strings.Index(got, "handle /api/live/*") >= strings.Index(got, "\n\thandle {") {
				t.Error("live route must precede the catch-all")
			}
			for _, block := range []struct {
				text string
				want bool
			}{{live, tc.live}, {reset, tc.reset}} {
				if tc.mode == "node" && block.text == live {
					continue
				}
				count := strings.Count(got, block.text)
				if count != boolCount(block.want) {
					t.Errorf("block count = %d, want %d: %s", count, boolCount(block.want), block.text)
				}
				if block.want {
					got = strings.Replace(got, block.text, "", 1)
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
