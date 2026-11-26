package templates_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestDockerBuildCache(t *testing.T) {
	t.Parallel()
	for _, pm := range []string{"pnpm", "bun"} {
		t.Run(pm, func(t *testing.T) {
			tree := testutil.RenderPack(t, []string{"base", "api", "web", "deploy", "jobs"}, map[string]any{
				"app_name": "todo", "module": "example.com/todo", "modules": "jobs", "package_manager": pm,
			})
			api := readDeployFile(t, tree, "deploy/Dockerfile.api")
			const compilerMount = "--mount=type=cache,target=/root/.cache/go-build"
			for _, binary := range []string{"api", "worker"} {
				want := "RUN " + compilerMount + " \\\n" +
					"    CGO_ENABLED=0 go build -trimpath -ldflags \"-s -w -X main.version=${VERSION}\" -o /out/" + binary + " ./cmd/" + binary
				if !strings.Contains(api, want+"\n") {
					t.Errorf("%s build must use the Go build cache", binary)
				}
			}

			ignore := readDeployFile(t, tree, "api/.dockerignore")
			for _, rule := range []string{"bin/", "tmp/", "internal/webdist/dist/"} {
				if !strings.Contains(ignore, "\n"+rule+"\n") {
					t.Errorf("API build context includes %s", rule)
				}
			}
			for _, name := range []string{"api/go.mod", "api/go.sum", "api/cmd/api/main.go"} {
				if _, err := fs.Stat(tree, name); err != nil {
					t.Error(err)
				}
			}

			web := readDeployFile(t, tree, "deploy/Dockerfile.web")
			prod := "pnpm install --prod --frozen-lockfile --ignore-scripts --offline"
			if pm == "bun" {
				prod = "bun install --production --frozen-lockfile --ignore-scripts"
			}
			want := "FROM dependencies AS production-dependencies\n" +
				"RUN rm -rf node_modules && " + prod + "\n\nFROM dependencies AS build\n"
			if !strings.Contains(web, want) || strings.Count(web, prod) != 1 {
				t.Error("production install must run once in the manifest-only stage")
			}
			if !strings.Contains(web, "\nCOPY --from=production-dependencies /app/node_modules ./node_modules\n") {
				t.Error("runtime image must copy node_modules from the production stage")
			}
		})
	}
}
