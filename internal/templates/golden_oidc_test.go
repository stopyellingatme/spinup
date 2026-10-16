package templates_test

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

// oidcGoldenFiles adds the enabled variant to the auth tree under oidc/.
func oidcGoldenFiles(t *testing.T, selected fstest.MapFS) error {
	t.Helper()
	all := testutil.RenderPack(t, []string{"base", "api", "web", "auth"}, map[string]any{
		"app_name": "todo", "module": "example.com/todo", "modules": "ci,auth", "auth_oidc": true,
	})
	for _, path := range []string{"api/db/migrations/00004_oidc_identities.sql", "web/src/lib/components/OidcButtons.svelte", "web/src/routes/login/+page.svelte"} {
		data, err := fs.ReadFile(all, path)
		if err != nil {
			return err
		}
		selected["oidc/"+path] = &fstest.MapFile{Data: data, Mode: 0o644}
	}
	return nil
}

func TestOIDCModuleCombinations(t *testing.T) {
	t.Parallel()
	for _, modules := range []string{"auth", "auth,mail"} {
		for _, enabled := range []bool{false, true} {
			t.Run(modules+"/"+map[bool]string{false: "off", true: "on"}[enabled], func(t *testing.T) {
				t.Parallel()
				packs := []string{"base", "api", "web", "auth"}
				if strings.Contains(modules, "mail") {
					packs = append(packs, "mail")
				}
				all := testutil.RenderPack(t, packs, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": modules, "auth_oidc": enabled})
				for _, path := range []string{"api/db/migrations/00004_oidc_identities.sql", "web/src/lib/components/OidcButtons.svelte"} {
					_, err := fs.Stat(all, path)
					if (err == nil) != enabled {
						t.Fatalf("%s: OIDC=%v, error=%v", path, enabled, err)
					}
				}
				page, err := fs.ReadFile(all, "web/src/routes/login/+page.svelte")
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(page), "<OidcButtons next={data.redirectTo} />") != enabled {
					t.Fatal("OIDC answer did not control login buttons")
				}
			})
		}
	}
}
