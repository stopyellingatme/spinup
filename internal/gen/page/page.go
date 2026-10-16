// Package page is the `spinup g page <route>` generator. It writes
// web/src/routes/<route>/+page.svelte, and with --server also +page.server.ts
// with a load function. The app needs the web pack. In an app with the auth
// module, the load function requires a signed-in user (ADR 0015 §5).
package page

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/pflag"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
	"github.com/stopyellingatme/spinup/internal/webtool"
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
	header, err := hasPageHeader(in.Root)
	if err != nil {
		return nil, err
	}
	dir := "web/src/routes/" + strings.Join(segments, "/")
	name := title(segments)
	files := []vfs.File{{Path: dir + "/+page.svelte", Data: pageSvelte(dir, name, server, header), Class: string(manifest.Scaffold)}}
	if server {
		auth := slices.Contains(in.Lock.Packs, "auth")
		files = append(files, vfs.File{Path: dir + "/+page.server.ts", Data: pageServerTS(name, auth), Class: string(manifest.Scaffold)})
	}
	var p vfs.Plan
	for _, f := range files {
		if err := p.Add(f); err != nil {
			return nil, err
		}
	}
	return &gen.Output{Plan: p, Next: []string{"cd web && " + webtool.FromLock(in.Lock).Run("check").String()}}, nil
}

// pageHeaderPath is the page header component of the web pack. Apps made
// before M12 lack it, and their pages use a plain heading.
const pageHeaderPath = "web/src/lib/components/app/page-header.svelte"

func hasPageHeader(root string) (bool, error) {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(pageHeaderPath)))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check %s: %w\nTry: check the file permissions", pageHeaderPath, err)
	}
	return true, nil
}

// pageSvelte returns the page. With a page header component, the page uses
// it. Otherwise the page has a plain heading.
func pageSvelte(dir, name string, server, header bool) []byte {
	var b strings.Builder
	edit := "<code>" + dir + "/+page.svelte</code>"
	heading := name
	if server {
		edit += " and <code>+page.server.ts</code>"
		heading = "{data.title}"
	}
	if server || header {
		b.WriteString("<script lang=\"ts\">\n")
		if header {
			b.WriteString("\timport PageHeader from '#lib/components/app/page-header.svelte';\n")
		}
		if server {
			b.WriteString("\timport type { PageProps } from './$types';\n\n\tlet { data }: PageProps = $props();\n")
		}
		b.WriteString("</script>\n\n")
	}
	fmt.Fprintf(&b, "<svelte:head>\n\t<title>%s</title>\n</svelte:head>\n\n", name)
	switch {
	case header && server:
		b.WriteString("<PageHeader title={data.title} />\n")
	case header:
		fmt.Fprintf(&b, "<PageHeader title=\"%s\" />\n", name)
	default:
		fmt.Fprintf(&b, "<h1 class=\"mb-2 text-2xl font-semibold\">%s</h1>\n", heading)
	}
	fmt.Fprintf(&b, "<p class=\"text-muted-foreground text-sm\">\n\tEdit %s to change this page.\n</p>\n", edit)
	return []byte(b.String())
}

func pageServerTS(name string, auth bool) []byte {
	imports, args, guard := "", "", ""
	if auth {
		imports = "import { requireUser } from '#lib/server/auth.js';\n"
		args = "{ locals, url }"
		guard = "\trequireUser(locals, url);\n"
	}
	return []byte(imports + `import type { PageServerLoad } from './$types';

// load runs on the server for each request of this page. To read from the Go API, use the
// typed client with the fetch of the load function:
//
//     const { data } = await api.GET('/api/...', { fetch, baseUrl: __PUBLIC_ORIGIN__ });
export const load: PageServerLoad = (` + args + `) => {
` + guard + `	return { title: '` + name + `' };
};
`)
}
