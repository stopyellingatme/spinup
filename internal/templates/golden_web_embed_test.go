package templates_test

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestGoldenWebEmbed(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "web-embed")
	render := func() (fs.FS, error) {
		all := testutil.RenderPack(t, []string{"base", "api", "web", "deploy"}, map[string]any{"app_name": "todo", "module": "example.com/todo", "frontend_mode": "embed"})
		selected := fstest.MapFS{}
		err := fs.WalkDir(all, ".", func(name string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			if !strings.HasPrefix(name, "web/") && !strings.HasPrefix(name, "deploy/") && !strings.HasPrefix(name, "api/internal/webdist/") && name != "api/cmd/api/main.go" {
				return nil
			}
			data, err := fs.ReadFile(all, name)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			selected[name] = &fstest.MapFile{Data: data, Mode: info.Mode()}
			return nil
		})
		return selected, err
	}
	files, err := render()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web/src/hooks.server.ts", "web/src/routes/+layout.server.ts", "web/src/routes/healthz/+server.ts", "deploy/Dockerfile.web"} {
		if _, err := fs.Stat(files, name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("embed app includes %s: %v", name, err)
		}
	}
	for _, name := range []string{"deploy/compose.yml", "deploy/Caddyfile", "deploy/environments/production.toml", "deploy/environments/staging.toml"} {
		data, err := fs.ReadFile(files, name)
		if err != nil {
			t.Fatal(err)
		}
		for _, absent := range []string{"WEB_IMAGE", "upstream.caddy web", "[resources.web]", "web = 2"} {
			if strings.Contains(string(data), absent) {
				t.Errorf("%s includes %s", name, absent)
			}
		}
	}
	testutil.Golden(t, "web-embed", files)
	testutil.Deterministic(t, render)
}
