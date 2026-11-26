package templates_test

import (
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

// TestAirConfig guards the dev loop settings of api/.air.toml: a fast link, one build for
// each query edit, and the paths that must stay watched.
func TestAirConfig(t *testing.T) {
	t.Parallel()
	tree := testutil.RenderPack(t, []string{"base", "api"}, map[string]any{
		"app_name": "todo", "module": "example.com/todo",
	})
	raw, err := fs.ReadFile(tree, "api/.air.toml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Build struct {
			Cmd              string   `toml:"cmd"`
			Delay            int      `toml:"delay"`
			IncludeExt       []string `toml:"include_ext"`
			ExcludeDir       []string `toml:"exclude_dir"`
			ExcludeUnchanged bool     `toml:"exclude_unchanged"`
		} `toml:"build"`
	}
	if err := toml.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	build := config.Build
	if build.Cmd != "go build -ldflags=-w -o ./tmp/api ./cmd/api" {
		t.Errorf("build cmd = %q, want the build without DWARF data", build.Cmd)
	}
	// The delay must batch the writes of one sqlc run, and stay short.
	if build.Delay < 50 || build.Delay > 150 {
		t.Errorf("delay = %d ms, want 50 to 150 ms", build.Delay)
	}
	// sqlc inputs start no build: the store code that sqlc writes does.
	if !slices.Contains(build.ExcludeDir, "db/queries") {
		t.Error("db/queries must not start a build")
	}
	// The binary embeds the migrations. The store code comes from sqlc, and internal/gen from oapi-codegen.
	for _, dir := range build.ExcludeDir {
		for _, watched := range []string{"internal/store", "internal/gen", "db/migrations", "db/kit", "cmd/api"} {
			if watched == dir || strings.HasPrefix(watched, dir+"/") {
				t.Errorf("exclude_dir %q hides %s", dir, watched)
			}
		}
	}
	if !slices.Contains(build.IncludeExt, "go") || !slices.Contains(build.IncludeExt, "sql") {
		t.Errorf("include_ext = %v, want go and sql", build.IncludeExt)
	}
	if !build.ExcludeUnchanged {
		t.Error("exclude_unchanged must stay on, or each sqlc run rebuilds the API")
	}
}
