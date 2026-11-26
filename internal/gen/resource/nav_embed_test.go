package resource

import (
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// TestPlanNavEmbed checks the navigation of an embed app (ADR 0039 §10.5): a
// resource has +page.svelte and form.ts. The sign-in, register, account, and
// reset pages never enter the navigation, though login and reset have a
// schema.ts. Planned files and removals count, and the list is sorted.
func TestPlanNavEmbed(t *testing.T) {
	t.Parallel()
	root := embedAuthApp(t)
	write(t, root, map[string]string{
		// Real resources on disk.
		"web/src/routes/zebras/+page.svelte": "<h1 />\n",
		"web/src/routes/zebras/form.ts":      "export {};\n",
		"web/src/routes/notes/+page.svelte":  "<h1 />\n",
		"web/src/routes/notes/form.ts":       "export {};\n",
		"web/src/routes/notes/schema.ts":     "export {};\n",
		// A resource that the caller removes.
		"web/src/routes/apples/+page.svelte": "<h1 />\n",
		"web/src/routes/apples/form.ts":      "export {};\n",
		// Pages that are not resources.
		"web/src/routes/reset/+page.svelte":   "<h1 />\n",
		"web/src/routes/reset/schema.ts":      "export {};\n",
		"web/src/routes/about/+page.svelte":   "<h1 />\n",
		"web/src/routes/settings/+page.ts":    "export {};\n",
		"web/src/routes/drafts/form.ts":       "export {};\n",
		"web/src/routes/blog-posts/schema.ts": "export {};\n",
	})
	plan := vfs.Plan{Files: []vfs.File{
		// A planned resource: its files are not on disk yet.
		{Path: "web/src/routes/blog-posts/+page.svelte", Data: []byte("<h1 />\n"), Class: string(manifest.Scaffold)},
		{Path: "web/src/routes/blog-posts/form.ts", Data: []byte("export {};\n"), Class: string(manifest.Scaffold)},
		// A planned deletion removes a resource marker.
		{Path: "web/src/routes/zebras/form.ts", Delete: true},
	}}
	got, err := PlanNav(root, plan, []string{"web/src/routes/apples/+page.svelte"})
	if err != nil {
		t.Fatal(err)
	}
	nav, ok := got.Get(navPath)
	if !ok || nav.Class != string(manifest.Managed) {
		t.Fatalf("navigation file = %#v, present = %v", nav, ok)
	}
	want := "export const nav: NavItem[] = [\n" +
		"\t{ href: '/blog-posts', label: 'Blog Posts' },\n" +
		"\t{ href: '/notes', label: 'Notes' },\n" +
		"];\nexport const home: string = '/blog-posts';\n"
	if !strings.HasSuffix(string(nav.Data), want) {
		t.Fatalf("navigation =\n%s\nwant suffix:\n%s", nav.Data, want)
	}
	for _, route := range []string{"login", "register", "account", "reset", "about", "settings", "drafts", "apples", "zebras"} {
		if strings.Contains(string(nav.Data), "'/"+route+"'") {
			t.Errorf("navigation lists /%s", route)
		}
	}
}

// TestPlanNavEmbedEmpty checks that an embed app without a resource has the
// home page / and an empty list, also with the sign-in pages on disk.
func TestPlanNavEmbedEmpty(t *testing.T) {
	t.Parallel()
	for name, root := range map[string]string{"no auth": embedApp(t, nil, nil), "auth": embedAuthApp(t)} {
		got, err := PlanNav(root, vfs.Plan{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		nav, ok := got.Get(navPath)
		if !ok || !strings.HasSuffix(string(nav.Data), "export const nav: NavItem[] = [\n];\nexport const home: string = '/';\n") {
			t.Errorf("%s: navigation = %s (planned %v)", name, nav.Data, ok)
		}
	}
}
