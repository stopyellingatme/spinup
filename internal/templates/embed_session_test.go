package templates_test

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

// plainEmbedLayoutTS is the root load of embed mode without auth, as the base commit renders it.
const plainEmbedLayoutTS = "// The embed app is a single-page app: the Go binary serves index.html for every page path.\n" +
	"export const ssr = false;\n"

// TestEmbedSession checks the root layout, the home load, the user menu, and the session helpers
// of embed mode (ADR 0039, sections 10.1 and 10.2) with auth and flags, with auth alone, and
// without auth. Without auth, the layouts keep the bytes of the plain single-page app.
func TestEmbedSession(t *testing.T) {
	t.Parallel()
	session := []string{
		"web/src/lib/auth.ts",
		"web/src/lib/session.svelte.ts",
		"web/src/lib/session-watch.ts",
		"web/src/lib/sign-out.ts",
		"web/src/lib/auth.test.ts",
		"web/src/lib/session-watch.test.ts",
		"web/src/lib/sign-out.test.ts",
	}
	authFiles := append([]string{"web/src/routes/+page.ts", "web/src/lib/components/app/user-menu.svelte"}, session...)
	nodeOnly := []string{"web/src/routes/+layout.server.ts", "web/src/routes/+page.server.ts", "web/src/hooks.server.ts"}

	type contains map[string][]string
	for _, tc := range []struct {
		name    string
		packs   []string
		modules string
		present []string
		absent  []string
		has     contains
		lacks   contains
	}{
		{
			name:    "auth and flags",
			packs:   []string{"base", "api", "web", "auth", "flags"},
			modules: "ci,auth,flags",
			present: authFiles,
			absent:  nodeOnly,
			has: contains{
				"web/src/routes/+layout.ts": {
					"export const ssr = false;",
					"depends(SESSION_DEP, FLAGS_DEP);",
					"await readSession(fetch)",
					"import '#lib/session-watch.js';",
					"sessionError: message",
				},
				"web/src/routes/+layout.svelte": {
					"onMount(watchSession);",
					"navigation.complete.finally(flushSessionCheck)",
					"session.changingUser",
					"data.sessionError === null",
					"onclick={recheckSession}",
					"import { adminRole } from '#lib/flags.gen.js';",
					"href: '/admin/flags'",
					"<title>todo</title>",
				},
				"web/src/routes/+page.ts":                     {"await parent()", "requireVerified();\n\tif (user && home !== '/') redirect(303, home);"},
				"web/src/lib/components/app/user-menu.svelte": {"void signOut()", "disabled={session.signingOut}", "links = []"},
				"web/src/lib/session.svelte.ts":               {"loadFlags(fetch)", "if (unverified !== null) error(503, unverified);"},
				// A guard decides only on a verified session (ADR 0039, section 10.10).
				"web/src/lib/auth.ts": {"requireVerified();\n\tif (!user) redirect(303, loginHref(url));", "requireVerified();\n\tif (user) redirect(303, home);"},
			},
			lacks: contains{
				"web/src/routes/+layout.svelte":               {"apikeys", "billing"},
				"web/src/lib/components/app/user-menu.svelte": {"/logout", "requestSubmit"},
			},
		},
		{
			name:    "auth",
			packs:   []string{"base", "api", "web", "auth"},
			modules: "ci,auth",
			present: authFiles,
			absent:  nodeOnly,
			has: contains{
				"web/src/routes/+layout.ts":     {"depends(SESSION_DEP, FLAGS_DEP);", "await readSession(fetch)"},
				"web/src/routes/+layout.svelte": {"onMount(watchSession);", "<UserMenu email={data.user.email} />"},
				"web/src/lib/session.svelte.ts": {"flags: {} };"},
			},
			lacks: contains{
				"web/src/routes/+layout.svelte": {"adminRole", "/admin/flags"},
				"web/src/lib/session.svelte.ts": {"loadFlags"},
			},
		},
		{
			name:    "no auth",
			packs:   []string{"base", "api", "web"},
			modules: "ci",
			absent:  append(append([]string{}, authFiles...), nodeOnly...),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tree := testutil.RenderPack(t, tc.packs, map[string]any{
				"app_name": "todo", "module": "example.com/todo", "modules": tc.modules, "frontend_mode": "embed",
			})
			for _, name := range tc.present {
				if _, err := fs.Stat(tree, name); err != nil {
					t.Errorf("missing %s: %v", name, err)
				}
			}
			for _, name := range tc.absent {
				if _, err := fs.Stat(tree, name); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("unexpected %s: %v", name, err)
				}
			}
			for name, wants := range tc.has {
				data := embedFile(t, tree, name)
				for _, want := range wants {
					if !strings.Contains(data, want) {
						t.Errorf("%s lacks %q", name, want)
					}
				}
			}
			for name, wants := range tc.lacks {
				data := embedFile(t, tree, name)
				for _, want := range wants {
					if strings.Contains(data, want) {
						t.Errorf("%s contains %q", name, want)
					}
				}
			}
		})
	}

	// Without auth, the layouts keep the bytes of the base commit: the plain root load, and the
	// layout that the node template renders for the same answers.
	t.Run("no auth bytes", func(t *testing.T) {
		t.Parallel()
		for _, appName := range []string{"todo", "ledger"} {
			answers := func(mode string) map[string]any {
				return map[string]any{"app_name": appName, "module": "example.com/x", "modules": "ci", "frontend_mode": mode}
			}
			embed := testutil.RenderPack(t, []string{"base", "api", "web"}, answers("embed"))
			node := testutil.RenderPack(t, []string{"base", "api", "web"}, answers("node"))
			if got := embedFile(t, embed, "web/src/routes/+layout.ts"); got != plainEmbedLayoutTS {
				t.Errorf("%s: +layout.ts = %q, want %q", appName, got, plainEmbedLayoutTS)
			}
			if got, want := embedFile(t, embed, "web/src/routes/+layout.svelte"), embedFile(t, node, "web/src/routes/+layout.svelte"); got != want {
				t.Errorf("%s: embed +layout.svelte differs from the plain layout:\n%s\nwant:\n%s", appName, got, want)
			}
		}
	})

	// Node mode keeps its menu, layout, and server loads, and gets none of the browser session files.
	t.Run("node auth", func(t *testing.T) {
		t.Parallel()
		tree := testutil.RenderPack(t, []string{"base", "api", "web", "auth"}, map[string]any{
			"app_name": "todo", "module": "example.com/todo", "modules": "ci,auth",
		})
		if menu := embedFile(t, tree, "web/src/lib/components/app/user-menu.svelte"); !strings.Contains(menu, `action="/logout"`) {
			t.Errorf("node user menu lacks the logout form:\n%s", menu)
		}
		for _, name := range []string{"web/src/routes/+layout.server.ts", "web/src/routes/+page.server.ts"} {
			if _, err := fs.Stat(tree, name); err != nil {
				t.Errorf("missing %s: %v", name, err)
			}
		}
		for _, name := range append([]string{"web/src/routes/+layout.ts", "web/src/routes/+page.ts"}, session...) {
			if _, err := fs.Stat(tree, name); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("unexpected %s: %v", name, err)
			}
		}
		if layout := embedFile(t, tree, "web/src/routes/+layout.svelte"); strings.Contains(layout, "session-watch") {
			t.Errorf("node +layout.svelte uses the session watch")
		}
	})
}

func embedFile(t *testing.T, tree fs.FS, name string) string {
	t.Helper()
	data, err := fs.ReadFile(tree, name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
