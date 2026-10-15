package webtool

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestCommands(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, pm, client                           string
		install, run, exec, dlx, refresh, generate string
	}{
		{"defaults", "", "", "pnpm install --frozen-lockfile --offline", "pnpm check", "pnpm exec vite", "pnpm dlx sv@1.0.0", "pnpm install --lockfile-only --prefer-offline --ignore-scripts", "pnpm exec openapi-typescript ../api/openapi/bundle.gen.yaml -o output"},
		{"bun heyapi", "bun", "heyapi", "bun install --frozen-lockfile", "bun run check", "bunx --no-install vite", "bunx sv@1.0.0", "bun install --lockfile-only", "bunx --no-install openapi-ts --input ../api/openapi/bundle.gen.yaml --output output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tool := For(map[string]any{"package_manager": tc.pm, "api_client": tc.client})
			for _, pair := range []struct{ got, want string }{
				{tool.Install(true, true).String(), tc.install}, {tool.Run("check").String(), tc.run}, {tool.Exec("vite").String(), tc.exec}, {tool.Dlx("sv@1.0.0").String(), tc.dlx}, {tool.RefreshLock().String(), tc.refresh}, {tool.Client("output").String(), tc.generate},
			} {
				if pair.got != pair.want {
					t.Errorf("got %q, want %q", pair.got, pair.want)
				}
			}
			cmd := tool.InWeb("/app", tool.Run("dev"))
			if tc.pm == "bun" {
				if cmd.Name != "bun" || cmd.Dir != filepath.Join("/app", "web") || !reflect.DeepEqual(cmd.Args, []string{"run", "dev"}) {
					t.Fatalf("bun web command: %+v", cmd)
				}
			} else if cmd.Dir != "/app" || !reflect.DeepEqual(cmd.Args, []string{"--dir", "web", "dev"}) {
				t.Fatalf("pnpm web command: %+v", cmd)
			}
		})
	}
}
