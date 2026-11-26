package templates_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestWebNodePerformance(t *testing.T) {
	for _, modules := range []string{"auth", "auth,flags"} {
		t.Run(modules, func(t *testing.T) {
			packs := []string{"base", "api", "web", "auth"}
			if strings.Contains(modules, "flags") {
				packs = append(packs, "flags")
			}
			tree := testutil.RenderPack(t, packs, map[string]any{
				"app_name": "todo", "module": "example.com/todo", "modules": modules, "frontend_mode": "node",
			})
			read := func(path string) string {
				t.Helper()
				b, err := fs.ReadFile(tree, path)
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			instrumentation := read("web/src/instrumentation.server.ts")
			branch := strings.Index(instrumentation, "if (endpoint) {")
			for _, name := range []string{"@opentelemetry/exporter-logs-otlp-http", "@opentelemetry/exporter-trace-otlp-http", "@opentelemetry/instrumentation-undici", "@opentelemetry/sdk-node", "./lib/server/log.js"} {
				pos := strings.Index(instrumentation, "await import('"+name+"'")
				if branch < 0 || pos < branch || strings.Count(instrumentation, name) != 1 {
					t.Errorf("%s must load only inside the endpoint branch", name)
				}
			}
			hooks := read("web/src/hooks.server.ts")
			for _, want := range []string{"kit/auth CookieName", "cookie.split(';').some((part) => part.trim().split('=', 1)[0] === 'session')", "headers: { cookie, accept: 'application/json' }"} {
				if !strings.Contains(hooks, want) {
					t.Errorf("hooks missing %q", want)
				}
			}
			if strings.Contains(hooks, "async function loadFlags") != strings.Contains(modules, "flags") {
				t.Error("flags rendering does not match modules")
			}
		})
	}
}
