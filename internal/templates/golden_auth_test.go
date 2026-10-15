package templates_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

// authGoldenPath selects the files of the auth pack and the API main file,
// which wires kit auth when the auth module is on.
func authGoldenPath(path string) bool {
	return path == "api/db/migrations/00002_create_auth.sql" ||
		path == "api/cmd/api/main.go" ||
		path == "web/src/lib/server/auth.ts" ||
		path == "web/src/lib/server/auth.test.ts" ||
		strings.HasPrefix(path, "web/src/routes/login/") ||
		strings.HasPrefix(path, "web/src/routes/register/") ||
		strings.HasPrefix(path, "web/src/routes/logout/") ||
		strings.HasPrefix(path, "web/src/routes/account/")
}

var authAnswers = map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": "ci,auth"}

func TestGoldenAuth(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "auth")
	render := func() (fs.FS, error) {
		all := testutil.RenderPack(t, []string{"base", "api", "web", "auth"}, authAnswers)
		selected := fstest.MapFS{}
		err := fs.WalkDir(all, ".", func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !authGoldenPath(path) {
				return err
			}
			data, err := fs.ReadFile(all, path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			selected[path] = &fstest.MapFile{Data: data, Mode: info.Mode()}
			return nil
		})
		if err == nil {
			err = oidcGoldenFiles(t, selected)
		}
		return selected, err
	}
	fsys, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "auth", fsys)
	testutil.Deterministic(t, render)

	main, err := fs.ReadFile(fsys, "api/cmd/api/main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"github.com/stopyellingatme/spinup/kit/auth"`,
		`auth.New(pool, auth.WithSecureCookie(cfg.Env != "dev"), auth.WithTrustedOrigins(web.PublicOrigin))`,
		"app.WithMiddleware(authn.Middleware())",
		"app.WithRoutes(authn.Routes)",
		"app.WithShutdown(authn.Close)",
	} {
		if !bytes.Contains(main, []byte(want)) {
			t.Errorf("api/cmd/api/main.go does not contain %s", want)
		}
	}
}

// TestAuthPackClasses checks the file classes of ADR 0008 §3: the migration is a seed, the
// pages are scaffolds, and the cookie helper and its test are managed.
func TestAuthPackClasses(t *testing.T) {
	t.Parallel()
	p, err := templates.Load(templates.Embedded(), "auth")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Requires) != 1 || p.Requires[0] != "web" {
		t.Errorf("requires = %v, want [web]", p.Requires)
	}
	for _, f := range p.Files {
		want := "scaffold"
		switch {
		case strings.HasPrefix(f.Path, "api/db/migrations/"), strings.HasPrefix(f.Path, "api/internal/mail/templates/"):
			want = "seed"
		case strings.HasPrefix(f.Path, "web/src/lib/server/"), f.Path == "api/cmd/api/reset.go":
			want = "managed"
		}
		if f.Class != want {
			t.Errorf("%s: class %q, want %q", f.Path, f.Class, want)
		}
	}
}

// TestAuthOffKeepsAPIMain checks that an app without the auth module gets the API main
// file of the api pack alone, byte for byte, and that auth and jobs combine.
func TestAuthOffKeepsAPIMain(t *testing.T) {
	t.Parallel()
	base := map[string]any{"app_name": "todo", "module": "example.com/todo"}
	without, err := fs.ReadFile(testutil.RenderPack(t, []string{"base", "api", "web"}, base), "api/cmd/api/main.go")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "testdata", "golden", "api", "api", "cmd", "api", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(without, want) {
		t.Fatal("api/cmd/api/main.go without the auth module differs from testdata/golden/api")
	}
	if bytes.Contains(without, []byte("auth")) {
		t.Fatal("api/cmd/api/main.go mentions auth without the auth module")
	}
	both := map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": "auth,jobs"}
	main, err := fs.ReadFile(testutil.RenderPack(t, []string{"base", "api", "web", "auth", "jobs"}, both), "api/cmd/api/main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"app.WithRoutes(authn.Routes)", "db.WithHook(jobs.MigrationHook())"} {
		if !bytes.Contains(main, []byte(want)) {
			t.Errorf("main.go with auth and jobs does not contain %s", want)
		}
	}
}

// TestAuthMigrationMatchesKit checks that the kit auth tests run on the schema that apps get.
func TestAuthMigrationMatchesKit(t *testing.T) {
	t.Parallel()
	pack, err := fs.ReadFile(templates.Embedded(), "auth/files/api/db/migrations/00002_create_auth.sql")
	if err != nil {
		t.Fatal(err)
	}
	kit, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "kit", "auth", "testdata", "migrations", "00002_create_auth.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pack, kit) {
		t.Fatal("kit/auth/testdata/migrations/00002_create_auth.sql differs from the auth pack migration\nTry: copy the pack migration to the kit testdata")
	}
}
