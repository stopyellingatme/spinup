package templates_test

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/render"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestGoldenMail(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "mail")
	render := func() (fs.FS, error) {
		all := testutil.RenderPack(t, []string{"base", "api", "web", "auth", "mail", "jobs"}, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": "auth,mail,jobs"})
		selected := fstest.MapFS{}
		err := fs.WalkDir(all, ".", func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			if !strings.HasPrefix(path, "api/internal/mail/") && path != "api/db/migrations/00003_password_resets.sql" && !strings.HasPrefix(path, "web/src/routes/reset/") && path != "api/cmd/api/main.go" && path != "api/cmd/api/reset.go" {
				return nil
			}
			data, err := fs.ReadFile(all, path)
			if err != nil {
				return err
			}
			selected[path] = &fstest.MapFile{Data: data, Mode: 0o644}
			return nil
		})
		return selected, err
	}
	files, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "mail", files)
	testutil.Deterministic(t, render)
}

func TestMailModuleCombinations(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		modules       string
		packs         []string
		worker, reset bool
	}{
		{"mail", []string{"base", "api", "mail"}, false, false},
		{"auth,mail", []string{"base", "api", "web", "auth", "mail"}, false, true},
		{"mail,jobs", []string{"base", "api", "mail", "jobs"}, true, false},
	} {
		t.Run(tc.modules, func(t *testing.T) {
			t.Parallel()
			all := testutil.RenderPack(t, tc.packs, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": tc.modules})
			_, err := fs.Stat(all, "api/internal/mail/worker.go")
			if (err == nil) != tc.worker {
				t.Fatalf("mail worker: %v", err)
			}
			_, err = fs.Stat(all, "api/cmd/api/reset.go")
			if (err == nil) != tc.reset {
				t.Fatalf("reset routes: %v", err)
			}
			main, err := fs.ReadFile(all, "api/cmd/api/main.go")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(main), "mailapp.New(pool)") {
				t.Fatal("missing mail initialization")
			}
		})
	}
}

// TestMailLoginTemplate checks the prepared source before the shared metadata selects it.
func TestMailLoginTemplate(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "without mail", true: "with mail"}[enabled], func(t *testing.T) {
			t.Parallel()
			base, err := templates.Load(templates.Embedded(), "base")
			if err != nil {
				t.Fatal(err)
			}
			base.Files = nil
			auth, err := templates.Load(templates.Embedded(), "auth")
			if err != nil {
				t.Fatal(err)
			}
			auth.Files = []templates.FileSpec{{Path: "web/src/routes/login/+page.svelte", Source: "files/web/src/routes/login/+page.svelte.tmpl", Class: "scaffold"}}
			modules := "auth"
			if enabled {
				modules += ",mail"
			}
			plan, err := render.Render(render.Input{Packs: []*templates.Pack{base, auth}, Answers: map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": modules}})
			if err != nil {
				t.Fatal(err)
			}
			file, ok := plan.Get("web/src/routes/login/+page.svelte")
			if !ok {
				t.Fatal("missing page")
			}
			if strings.Contains(string(file.Data), "Forgot password?") != enabled {
				t.Fatal("mail condition did not control the link")
			}
		})
	}
}
