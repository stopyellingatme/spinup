package templates_test

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

// TestEmbedFlagsLive checks the flag admin page and the live helper of embed mode (ADR 0039,
// sections 10.1, 10.2, and 10.3). Embed mode renders the browser files and no server file of the
// two packs. Node mode keeps its files. Both modes share the schema, the flag helpers, and the
// live core.
func TestEmbedFlagsLive(t *testing.T) {
	t.Parallel()
	render := func(mode string) fs.FS {
		return testutil.RenderPack(t, []string{"base", "api", "web", "flags", "live"}, map[string]any{
			"app_name": "todo", "module": "example.com/todo", "modules": "ci,auth,flags,live", "frontend_mode": mode,
		})
	}
	embed := render("embed")
	node := render("node")

	shared := []string{
		"web/src/routes/admin/flags/schema.ts",
		"web/src/lib/flags.ts",
		"web/src/lib/flags.gen.ts",
		"web/src/lib/live.core.ts",
		"web/src/lib/live.core.test.ts",
	}
	embedOnly := []string{
		"web/src/lib/flags-admin.ts",
		"web/src/routes/admin/flags/+page.ts",
		"web/src/lib/live-session.test.ts",
	}
	nodeOnly := []string{
		"web/src/lib/server/flags.ts",
		"web/src/routes/admin/flags/+page.server.ts",
	}
	both := []string{"web/src/routes/admin/flags/+page.svelte", "web/src/lib/live.svelte.ts"}

	for _, name := range append(append([]string{}, shared...), append(embedOnly, both...)...) {
		embedFile(t, embed, name)
	}
	for _, name := range append(append([]string{}, shared...), append(nodeOnly, both...)...) {
		embedFile(t, node, name)
	}
	for _, name := range nodeOnly {
		if _, err := fs.Stat(embed, name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("embed render has %s: %v", name, err)
		}
	}
	for _, name := range embedOnly {
		if _, err := fs.Stat(node, name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("node render has %s: %v", name, err)
		}
	}
	for _, name := range shared {
		if embedFile(t, embed, name) != embedFile(t, node, name) {
			t.Errorf("%s differs between embed mode and node mode", name)
		}
	}

	// An embed app is a single-page app: no file of these packs runs on a server.
	err := fs.WalkDir(embed, "web", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(name, "/lib/server/") || strings.HasSuffix(name, "/+page.server.ts") {
			t.Errorf("embed render has the server file %s", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	type contains map[string][]string
	check := func(tree fs.FS, has, lacks contains) {
		t.Helper()
		for name, wants := range has {
			data := embedFile(t, tree, name)
			for _, want := range wants {
				if !strings.Contains(data, want) {
					t.Errorf("%s lacks %q", name, want)
				}
			}
		}
		for name, wants := range lacks {
			data := embedFile(t, tree, name)
			for _, want := range wants {
				if strings.Contains(data, want) {
					t.Errorf("%s contains %q", name, want)
				}
			}
		}
	}
	check(embed, contains{
		"web/src/lib/flags-admin.ts": {
			"export const FLAGS_ADMIN_DEP = 'app:flags-admin';",
			"observedFetch(fetch)(path",
			"export function listFlags(fetch: typeof globalThis.fetch): Promise<FlagResult<FlagsAdmin>>",
			"export function putRule(fetch: typeof globalThis.fetch, key: string, rule: FlagRule): Promise<FlagResult<FlagEntry>>",
			"export function clearUser(fetch: typeof globalThis.fetch, key: string, email: string): Promise<FlagResult<void>>",
			"'GET', '/api/admin/flags'",
			"encodeURIComponent(email)",
		},
		"web/src/routes/admin/flags/+page.ts": {
			"depends(FLAGS_ADMIN_DEP);",
			"const { user } = await parent();",
			"requireRole(user, url, adminRole);",
			"import { adminRole } from '#lib/flags.gen.js';",
		},
		"web/src/routes/admin/flags/+page.svelte": {
			"url.href === FLAGS_ADMIN_DEP || url.href === FLAGS_DEP",
			"from './schema'",
			"} finally {",
			"<title>Feature flags</title>",
			"Save rule",
			"Add user rule",
			"User rules",
			"Stale flags",
		},
		"web/src/lib/live.svelte.ts": {
			"onSessionPause(",
			"recheckSession();",
			"export function liveRefresh(feed: string, options: LiveRefreshOptions = {}): LiveState",
			"export function liveList<T extends { id: number | string }>(feed: string, options: LiveListOptions<T> = {}): LiveList<T>",
			"await invalidate(options.invalidate)",
		},
		"web/src/lib/live-session.test.ts": {"await import('./sign-out.js')", "'POST /api/auth/logout'"},
	}, contains{
		"web/src/lib/flags-admin.ts":              {"__PUBLIC_ORIGIN__", "#lib/server"},
		"web/src/routes/admin/flags/+page.ts":     {"locals"},
		"web/src/routes/admin/flags/+page.svelte": {"$app/forms", "use:enhance", "action=\"?/"},
	})
	// The guard runs before the admin read.
	load := embedFile(t, embed, "web/src/routes/admin/flags/+page.ts")
	if strings.Index(load, "requireRole(") > strings.Index(load, "await listFlags(") {
		t.Errorf("the admin load reads the flags before the role guard:\n%s", load)
	}
	// A 401 of the live helper never refreshes all page data. Only the default refresh does.
	liveTS := embedFile(t, embed, "web/src/lib/live.svelte.ts")
	if n := strings.Count(liveTS, "invalidateAll()"); n != 1 {
		t.Errorf("embed live.svelte.ts calls invalidateAll() %d times, want 1 (the default page refresh)", n)
	}

	check(node, contains{
		"web/src/routes/admin/flags/+page.svelte": {"use:enhance", "action=\"?/rule\""},
		"web/src/lib/live.svelte.ts":              {"onUnauthorized: () => { void invalidateAll()"},
	}, contains{
		"web/src/lib/live.svelte.ts": {"onSessionPause", "session-watch"},
	})
}
