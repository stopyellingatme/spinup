package templates_test

import (
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestGoldenBilling(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "billing")
	pack, err := templates.Load(templates.Embedded(), "billing")
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{
		"api/cmd/api/main.go", "api/cmd/worker/main.go", "deploy/compose.yml",
		"web/src/lib/components/app/user-menu.svelte", "web/src/routes/+layout.server.ts",
	}
	for _, file := range pack.Files {
		paths = append(paths, file.Path)
	}
	render := func() (fs.FS, error) {
		selected := fstest.MapFS{}
		for _, keys := range []bool{false, true} {
			packs := []string{"base", "api", "web", "deploy", "auth", "jobs", "billing"}
			modules := "auth,jobs,billing"
			prefix := ""
			if keys {
				packs = append(packs, "apikeys")
				modules += ",apikeys"
				prefix = "apikeys"
			}
			all := testutil.RenderPack(t, packs, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": modules})
			for _, path := range paths {
				data, err := fs.ReadFile(all, path)
				if err != nil {
					return nil, err
				}
				selected[filepath.ToSlash(filepath.Join(prefix, path))] = &fstest.MapFile{Data: data, Mode: 0o644}
			}
		}
		return selected, nil
	}
	fsys, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "billing", fsys)
	testutil.Deterministic(t, render)
	for _, prefix := range []string{"", "apikeys"} {
		for path, want := range map[string][]string{
			"api/cmd/api/main.go":              {"kitbilling.New", "kitbilling.WithRoles(roles.Set)", "kitbilling.WithPublicOrigin(web.PublicOrigin)", "kitbilling.WithWebhookSecrets(billingEnv.WebhookSecret)", "billingSvc.Routes(authn)", `app.WithCommand("billing", billingSvc.Command())`},
			"api/cmd/worker/main.go":           {"kitbilling.New", "kitbilling.WithRoles(roles.Set)", "kitbilling.WithPublicOrigin(billingEnv.PublicOrigin)", "billingSvc.Register(w)"},
			"deploy/compose.yml":               {"STRIPE_SECRET_KEY_FILE: /run/secrets/STRIPE_SECRET_KEY", "STRIPE_WEBHOOK_SECRET_FILE: /run/secrets/STRIPE_WEBHOOK_SECRET"},
			"web/src/routes/+layout.server.ts": {"locals.user ? await getBilling(fetch) : null"},
		} {
			data, err := fs.ReadFile(fsys, filepath.ToSlash(filepath.Join(prefix, path)))
			if err != nil {
				t.Fatal(err)
			}
			if prefix != "" && slices.Contains([]string{"api/cmd/api/main.go", "api/cmd/worker/main.go"}, path) {
				want = append(want, "apikeys.WithSessionOnly(kitbilling.SessionPrefix)", "kitbilling.WithAPIKeys(keyStore)")
			}
			for _, part := range want {
				if !strings.Contains(string(data), part) {
					t.Errorf("%s/%s missing %q", prefix, path, part)
				}
			}
		}
	}
}

// TestBillingOpenAPI checks that the kit routes stay outside the app spec, like
// the auth and apikeys routes. oapi-codegen would otherwise ask the app for handlers.
func TestBillingOpenAPI(t *testing.T) {
	t.Parallel()
	tree := testutil.RenderPack(t, []string{"base", "api"}, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": []string{"auth", "jobs", "billing"}})
	spec, err := fs.ReadFile(tree, "api/openapi/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(spec), "billing") {
		t.Error("the billing routes must stay outside the app spec")
	}
}
