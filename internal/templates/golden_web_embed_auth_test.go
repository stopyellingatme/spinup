package templates_test

import (
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

// embedAuthPacks is the explicit pack selection of the embed app with sign-in (ADR 0039, section 10.6).
var embedAuthPacks = []string{"base", "api", "web", "ci", "auth", "flags", "live"}

// embedAuthGoldenFiles are the browser twins, the shared admin schema, the root shell, the menu,
// and the rendered guidance. The duplicate UI vendor files stay out of the snapshot.
var embedAuthGoldenFiles = []string{
	"AGENTS.md",
	"web/package.json",
	"web/vite.config.js",
	"web/src/routes/+layout.ts",
	"web/src/routes/+layout.svelte",
	"web/src/routes/+page.ts",
	"web/src/lib/components/app/user-menu.svelte",
	"web/src/lib/auth.ts",
	"web/src/lib/session-watch.ts",
	"web/src/lib/sign-out.ts",
	"web/src/lib/session.svelte.ts",
	"web/src/lib/auth.test.ts",
	"web/src/lib/session-watch.test.ts",
	"web/src/lib/sign-out.test.ts",
	"web/src/lib/auth-form.ts",
	"web/src/routes/login/+page.ts",
	"web/src/routes/register/+page.ts",
	"web/src/routes/account/+page.ts",
	"web/src/routes/login/+page.svelte",
	"web/src/routes/register/+page.svelte",
	"web/src/routes/account/+page.svelte",
	"web/src/routes/login/schema.ts",
	"web/src/lib/flags-admin.ts",
	"web/src/routes/admin/flags/+page.ts",
	"web/src/routes/admin/flags/+page.svelte",
	"web/src/routes/admin/flags/schema.ts",
	"web/src/lib/live.svelte.ts",
	"web/src/lib/live-session.test.ts",
}

// embedNodeOnlyFiles are the files of node mode that an embed render must never hold.
var embedNodeOnlyFiles = []string{
	"web/src/hooks.server.ts",
	"web/src/routes/+layout.server.ts",
	"web/src/routes/healthz/+server.ts",
	"web/src/lib/server/auth.ts",
	"web/src/lib/server/auth.test.ts",
	"web/src/lib/server/flags.ts",
	"web/src/routes/login/+page.server.ts",
	"web/src/routes/register/+page.server.ts",
	"web/src/routes/account/+page.server.ts",
	"web/src/routes/logout/+page.server.ts",
	"web/src/routes/admin/flags/+page.server.ts",
	"web/src/routes/reset/+page.server.ts",
	"web/src/routes/reset/[token]/+page.server.ts",
	"web/src/routes/reset/[token]/+page.svelte",
}

func embedAuthAnswers(modules string, oidc bool) map[string]any {
	return map[string]any{
		"app_name": "todo", "module": "example.com/todo", "modules": modules, "frontend_mode": "embed",
		"package_manager": "pnpm", "agent_guidance": true, "auth_oidc": oidc,
	}
}

// checkEmbedRender asserts the absence of node-only files against the full render, before any filter.
func checkEmbedRender(t *testing.T, all fs.FS) {
	t.Helper()
	for _, name := range embedNodeOnlyFiles {
		if _, err := fs.Stat(all, name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("embed render holds the node file %s: %v", name, err)
		}
	}
	if err := fs.WalkDir(all, "web", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		base := name[strings.LastIndex(name, "/")+1:]
		if strings.HasSuffix(base, ".server.ts") || base == "+server.ts" || strings.HasPrefix(name, "web/src/lib/server/") {
			t.Errorf("embed render holds the server file %s", name)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGoldenWebEmbedAuth(t *testing.T) {
	t.Parallel()
	if !testutil.GoldenSelected(t, "web-embed-auth") {
		return
	}
	render := func() (fs.FS, error) {
		all := testutil.RenderPack(t, embedAuthPacks, embedAuthAnswers("ci,auth,flags,live", false))
		checkEmbedRender(t, all)
		selected := fstest.MapFS{}
		for _, name := range embedAuthGoldenFiles {
			data, err := fs.ReadFile(all, name)
			if err != nil {
				return nil, err
			}
			selected[name] = &fstest.MapFile{Data: data, Mode: 0o644}
		}
		return selected, nil
	}
	files, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "web-embed-auth", files)
	testutil.Deterministic(t, render)
}

// TestEmbedAuthOptionalFiles renders the optional files of the mail module and OIDC in embed mode.
// The golden tree holds neither, so these render cases check them.
func TestEmbedAuthOptionalFiles(t *testing.T) {
	t.Parallel()
	resetFiles := []string{
		"web/src/routes/reset/+page.ts",
		"web/src/routes/reset/+page.svelte",
		"web/src/routes/reset/schema.ts",
		"web/src/routes/reset/confirm/+page.ts",
		"web/src/routes/reset/confirm/+page.svelte",
		"web/src/routes/reset/confirm/schema.ts",
		"web/src/lib/reset-token.ts",
		"web/src/lib/reset-token.test.ts",
		"api/cmd/api/reset.go",
	}
	const oidcButtons = "web/src/lib/components/OidcButtons.svelte"
	for _, tc := range []struct {
		name    string
		packs   []string
		modules string
		oidc    bool
	}{
		{name: "mail", packs: append(slices.Clone(embedAuthPacks), "mail"), modules: "ci,auth,flags,live,mail"},
		{name: "oidc", packs: embedAuthPacks, modules: "ci,auth,flags,live", oidc: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			all := testutil.RenderPack(t, tc.packs, embedAuthAnswers(tc.modules, tc.oidc))
			checkEmbedRender(t, all)
			mail := strings.Contains(tc.modules, "mail")
			for _, name := range resetFiles {
				if _, err := fs.Stat(all, name); (err == nil) != mail {
					t.Errorf("%s: present=%v, want %v", name, err == nil, mail)
				}
			}
			if _, err := fs.Stat(all, oidcButtons); (err == nil) != tc.oidc {
				t.Errorf("%s: present=%v, want %v", oidcButtons, err == nil, tc.oidc)
			}
			login, err := fs.ReadFile(all, "web/src/routes/login/+page.svelte")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(login), `href="/reset"`) != mail {
				t.Errorf("login reset link: want %v", mail)
			}
			if strings.Contains(string(login), "spinup:oidc-pending") != tc.oidc || strings.Contains(string(login), "OidcButtons") != tc.oidc {
				t.Errorf("login OIDC markup: want %v", tc.oidc)
			}
			main, err := fs.ReadFile(all, "api/cmd/api/main.go")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(main), "resetPageHeaders") {
				t.Error("main.go wires the removed reset page headers")
			}
			// The other selected files keep the bytes of the plain auth, flags, and live render.
			base := testutil.RenderPack(t, embedAuthPacks, embedAuthAnswers("ci,auth,flags,live", false))
			for _, name := range []string{"web/src/lib/session.svelte.ts", "web/src/lib/live.svelte.ts", "web/src/lib/flags-admin.ts"} {
				want, _ := fs.ReadFile(base, name)
				got, err := fs.ReadFile(all, name)
				if err != nil || string(got) != string(want) {
					t.Errorf("%s changed with %s: %v", name, tc.name, err)
				}
			}
		})
	}
}
