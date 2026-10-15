// Package page is the `spinup g page <route>` generator. It writes
// web/src/routes/<route>/+page.svelte, and with --server also +page.server.ts
// with a load function. The app needs the web pack.
package page

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/pflag"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

func init() {
	registry.Register(gen.Generator{
		Name:  "page",
		Short: "Generate a SvelteKit page in web/src/routes",
		Usage: "<route>",
		Flags: func(fs *pflag.FlagSet) {
			fs.Bool("server", false, "also write +page.server.ts with a load function")
		},
		Run: plan,
	})
}

var (
	staticSegment = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	paramSegment  = regexp.MustCompile(`^\[[a-zA-Z_][a-zA-Z0-9_]*\]$`)
	groupSegment  = regexp.MustCompile(`^\([a-z0-9]+(-[a-z0-9]+)*\)$`)
)

// ParseRoute validates a route such as "about", "settings/profile", or
// "posts/[slug]", and returns its segments. A segment is lower-case kebab
// case, a parameter such as [id], or a group such as (marketing). Leading and
// trailing slashes are ignored. Routes under /api belong to the Go API.
func ParseRoute(route string) ([]string, error) {
	trimmed := strings.Trim(route, "/")
	if trimmed == "" {
		return nil, fmt.Errorf("route %q is empty: the home page is web/src/routes/+page.svelte\nTry: spinup g page about", route)
	}
	segments := strings.Split(trimmed, "/")
	for _, s := range segments {
		if !staticSegment.MatchString(s) && !paramSegment.MatchString(s) && !groupSegment.MatchString(s) {
			return nil, fmt.Errorf("route %q has the invalid segment %q: use lower-case letters, digits, and hyphens, a parameter such as [id], or a group such as (app)\nTry: spinup g page settings/profile", route, s)
		}
	}
	first := segments[0]
	for _, s := range segments {
		if !groupSegment.MatchString(s) {
			first = s
			break
		}
	}
	if first == "api" {
		return nil, fmt.Errorf("route %q is under /api, which goes to the Go API\nTry: choose another route, or add an API operation in api/openapi/", route)
	}
	return segments, nil
}

// title returns the page title: the last plain segment in words, with an
// upper-case first letter.
func title(segments []string) string {
	for i := len(segments) - 1; i >= 0; i-- {
		if staticSegment.MatchString(segments[i]) {
			words := strings.ReplaceAll(segments[i], "-", " ")
			return strings.ToUpper(words[:1]) + words[1:]
		}
	}
	return "Page"
}

func plan(_ context.Context, in gen.Input) (*gen.Output, error) {
	if len(in.Args) != 1 {
		return nil, fmt.Errorf("expected one route\nTry: spinup g page <route> (for example: spinup g page settings/profile)")
	}
	if in.Lock == nil || !slices.Contains(in.Lock.Packs, "web") {
		return nil, fmt.Errorf("the app has no web pack, so it has no web/src/routes\nTry: generate pages in an app made without --api-only")
	}
	segments, err := ParseRoute(in.Args[0])
	if err != nil {
		return nil, err
	}
	server := false
	if in.Flags != nil {
		server, _ = in.Flags.GetBool("server")
	}
	if server && in.Lock.Answers["frontend_mode"] == "embed" {
		return nil, fmt.Errorf("cannot generate a server page in embed mode: +page.server.ts needs a SvelteKit server\nTry: omit --server, or use an app with --frontend-mode node")
	}
	dir := "web/src/routes/" + strings.Join(segments, "/")
	name := title(segments)
	files := []vfs.File{{Path: dir + "/+page.svelte", Data: pageSvelte(dir, name, server), Class: string(manifest.Scaffold)}}
	if server {
		files = append(files, vfs.File{Path: dir + "/+page.server.ts", Data: pageServerTS(name), Class: string(manifest.Scaffold)})
	}
	var p vfs.Plan
	for _, f := range files {
		if err := p.Add(f); err != nil {
			return nil, err
		}
	}
	return &gen.Output{Plan: p, Next: []string{"cd web && pnpm check"}}, nil
}

func pageSvelte(dir, name string, server bool) []byte {
	var b strings.Builder
	heading := name
	edit := "<code>" + dir + "/+page.svelte</code>"
	if server {
		heading = "{data.title}"
		edit += " and <code>+page.server.ts</code>"
		b.WriteString(`<script lang="ts">
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();
</script>

`)
	}
	fmt.Fprintf(&b, `<svelte:head>
	<title>%s</title>
</svelte:head>

<h1 class="text-2xl font-semibold">%s</h1>
<p class="text-muted-foreground mt-2 text-sm">
	Edit %s to change this page.
</p>
`, name, heading, edit)
	return []byte(b.String())
}

func pageServerTS(name string) []byte {
	return []byte(`import type { PageServerLoad } from './$types';

// load runs on the server for each request of this page. To read from the Go API, use the
// typed client with the fetch of the load function:
//
//     const { data } = await api.GET('/api/...', { fetch, baseUrl: __PUBLIC_ORIGIN__ });
export const load: PageServerLoad = () => {
	return { title: '` + name + `' };
};
`)
}
