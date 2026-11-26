package page

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func TestParseRoute(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want []string
		err  string
	}{
		{in: "about", want: []string{"about"}},
		{in: "/settings/profile/", want: []string{"settings", "profile"}},
		{in: "posts/[slug]", want: []string{"posts", "[slug]"}},
		{in: "(marketing)/pricing-plans", want: []string{"(marketing)", "pricing-plans"}},
		{in: "v2", want: []string{"v2"}},
		{in: "", err: "is empty"},
		{in: "/", err: "is empty"},
		{in: "About", err: `invalid segment "About"`},
		{in: "a//b", err: `invalid segment ""`},
		{in: "../x", err: `invalid segment ".."`},
		{in: "+page", err: `invalid segment "+page"`},
		{in: "a_b", err: `invalid segment "a_b"`},
		{in: "-a", err: `invalid segment "-a"`},
		{in: "[1id]", err: `invalid segment "[1id]"`},
		{in: "api/x", err: "goes to the Go API"},
		{in: "(app)/api", err: "goes to the Go API"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := ParseRoute(tt.in)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) || !strings.Contains(err.Error(), "\nTry: ") {
					t.Fatalf("ParseRoute(%q) error %v, want %q and a Try line", tt.in, err, tt.err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseRoute(%q) = %v, %v", tt.in, got, err)
			}
		})
	}
}

func webLock() *manifest.Lock {
	lock := manifest.New()
	lock.Packs = []string{"base", "api", "web"}
	return lock
}

func flags(t *testing.T, args ...string) *pflag.FlagSet {
	t.Helper()
	fs := pflag.NewFlagSet("page", pflag.ContinueOnError)
	fs.Bool("server", false, "")
	fs.String("role", "", "")
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestPlan(t *testing.T) {
	t.Parallel()
	out, err := plan(context.Background(), gen.Input{Lock: webLock(), Args: []string{"settings/profile"}, Flags: flags(t)})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Plan.Paths(); !reflect.DeepEqual(got, []string{"web/src/routes/settings/profile/+page.svelte"}) {
		t.Fatalf("paths %v", got)
	}
	f, _ := out.Plan.Get("web/src/routes/settings/profile/+page.svelte")
	if f.Class != string(manifest.Scaffold) || !strings.Contains(string(f.Data), "<title>Profile</title>") || strings.Contains(string(f.Data), "<script") {
		t.Fatalf("page %s:\n%s", f.Class, f.Data)
	}
	if !reflect.DeepEqual(out.Next, []string{"cd web && pnpm check"}) || len(out.PostSteps) != 0 || len(out.Commands) != 0 {
		t.Fatalf("output %+v", out)
	}

	out, err = plan(context.Background(), gen.Input{Lock: webLock(), Args: []string{"posts/[slug]"}, Flags: flags(t, "--server")})
	if err != nil {
		t.Fatal(err)
	}
	page, _ := out.Plan.Get("web/src/routes/posts/[slug]/+page.svelte")
	server, ok := out.Plan.Get("web/src/routes/posts/[slug]/+page.server.ts")
	if !ok || server.Class != string(manifest.Scaffold) || !strings.Contains(string(server.Data), "return { title: 'Posts' };") {
		t.Fatalf("server file %v:\n%s", ok, server.Data)
	}
	if !strings.Contains(string(page.Data), "let { data }: PageProps = $props();") || !strings.Contains(string(page.Data), "{data.title}") {
		t.Fatalf("page:\n%s", page.Data)
	}
}

func TestPlanRefusals(t *testing.T) {
	t.Parallel()
	apiOnly := manifest.New()
	apiOnly.Packs = []string{"base", "api"}
	tests := []struct {
		name string
		in   gen.Input
		want string
	}{
		{"no route", gen.Input{Lock: webLock()}, "expected one route"},
		{"two routes", gen.Input{Lock: webLock(), Args: []string{"a", "b"}}, "expected one route"},
		{"no web", gen.Input{Lock: apiOnly, Args: []string{"about"}}, "no web pack"},
		{"bad route", gen.Input{Lock: webLock(), Args: []string{"About"}}, "invalid segment"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := plan(context.Background(), tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("error %v, want %q", err, tc.want)
			}
		})
	}
}

func TestTitle(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"about": "About", "pricing-plans/[id]": "Pricing plans", "[id]": "Page", "(app)": "Page"} {
		segments, err := ParseRoute(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := title(segments); got != want {
			t.Errorf("title(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEmbedPage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		auth   bool
		args   []string
		want   []string
		absent []string
	}{
		{name: "plain", absent: []string{"+page.ts"}},
		{name: "load", args: []string{"--server"}, want: []string{"export const load: PageLoad = () => {", "return { title: 'Pricing plans' };"}, absent: []string{"requireUser", "parent()"}},
		{name: "auth", auth: true, args: []string{"--server"}, want: []string{"import { requireUser } from '#lib/auth';", "const { user } = await parent();", "requireUser(user, url);"}, absent: []string{"requireRole"}},
		{name: "role", auth: true, args: []string{"--server", "--role", "manager,staff"}, want: []string{"import { requireRole } from '#lib/auth';", "const { user } = await parent();", "requireRole(user, url, 'manager', 'staff');"}, absent: []string{"requireUser"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lock := webLock()
			lock.Answers["frontend_mode"] = "embed"
			if tc.auth {
				lock.Packs = append(lock.Packs, "auth")
			}
			out, err := plan(t.Context(), gen.Input{Root: t.TempDir(), Lock: lock, Args: []string{"pricing-plans/[id]"}, Flags: flags(t, tc.args...)})
			if err != nil {
				t.Fatal(err)
			}
			dir := "web/src/routes/pricing-plans/[id]/"
			page, ok := out.Plan.Get(dir + "+page.svelte")
			if !ok || page.Class != string(manifest.Scaffold) {
				t.Fatal("missing scaffold component")
			}
			load, hasLoad := out.Plan.Get(dir + "+page.ts")
			if hasLoad != (len(tc.args) > 0) || len(out.Plan.Files) != map[bool]int{false: 1, true: 2}[hasLoad] {
				t.Fatalf("paths: %v", out.Plan.Paths())
			}
			if hasLoad && (load.Class != string(manifest.Scaffold) || !strings.Contains(string(page.Data), "<code>+page.ts</code>")) {
				t.Fatalf("files: %s %s", page.Data, load.Data)
			}
			if _, ok := out.Plan.Get(dir + "+page.server.ts"); ok {
				t.Fatal("server load exists")
			}
			text := string(page.Data) + string(load.Data)
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Errorf("missing %q in %s", want, text)
				}
			}
			for _, absent := range append(tc.absent, "$lib/server", "#lib/server") {
				if strings.Contains(text, absent) {
					t.Errorf("unexpected %q in %s", absent, text)
				}
			}
		})
	}
}

// TestPlanShellAndAuth checks that a page uses the page header when the app has
// it, and that a server page of an app with auth requires a signed-in user.
func TestPlanShellAndAuth(t *testing.T) {
	t.Parallel()
	shell := t.TempDir()
	path := filepath.Join(shell, filepath.FromSlash(pageHeaderPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("<h1 />\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	authLock := webLock()
	authLock.Packs = append(authLock.Packs, "auth")
	tests := []struct {
		name   string
		root   string
		lock   *manifest.Lock
		page   []string
		server []string
		absent []string
	}{
		{
			name:   "old app",
			root:   t.TempDir(),
			lock:   webLock(),
			page:   []string{`<h1 class="mb-2 text-2xl font-semibold">{data.title}</h1>`},
			server: []string{"export const load: PageServerLoad = () => {"},
			absent: []string{"PageHeader", "requireUser"},
		},
		{
			name:   "shell",
			root:   shell,
			lock:   webLock(),
			page:   []string{"import PageHeader from '#lib/components/app/page-header.svelte';", "<PageHeader title={data.title} />"},
			server: []string{"export const load: PageServerLoad = () => {"},
			absent: []string{"<h1", "requireUser", "#lib/server/auth"},
		},
		{
			name: "auth",
			root: shell,
			lock: authLock,
			page: []string{"<PageHeader title={data.title} />"},
			server: []string{
				"import { requireUser } from '#lib/server/auth.js';",
				"export const load: PageServerLoad = ({ locals, url }) => {\n\trequireUser(locals, url);\n\treturn { title: 'Profile' };",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := plan(t.Context(), gen.Input{Root: tc.root, Lock: tc.lock, Args: []string{"settings/profile"}, Flags: flags(t, "--server")})
			if err != nil {
				t.Fatal(err)
			}
			page, _ := out.Plan.Get("web/src/routes/settings/profile/+page.svelte")
			server, _ := out.Plan.Get("web/src/routes/settings/profile/+page.server.ts")
			for _, w := range tc.page {
				if !strings.Contains(string(page.Data), w) {
					t.Errorf("page has no %q:\n%s", w, page.Data)
				}
			}
			for _, w := range tc.server {
				if !strings.Contains(string(server.Data), w) {
					t.Errorf("server file has no %q:\n%s", w, server.Data)
				}
			}
			for _, w := range tc.absent {
				if strings.Contains(string(page.Data)+string(server.Data), w) {
					t.Errorf("files contain %q:\n%s\n%s", w, page.Data, server.Data)
				}
			}
		})
	}

	// A client page in an app with the shell uses the header with a fixed title.
	out, err := plan(t.Context(), gen.Input{Root: shell, Lock: authLock, Args: []string{"about"}, Flags: flags(t)})
	if err != nil {
		t.Fatal(err)
	}
	page, _ := out.Plan.Get("web/src/routes/about/+page.svelte")
	if !strings.Contains(string(page.Data), `<PageHeader title="About" />`) || strings.Contains(string(page.Data), "$props") {
		t.Fatalf("client page:\n%s", page.Data)
	}
}

func TestRolePage(t *testing.T) {
	t.Parallel()
	lock := webLock()
	lock.Packs = append(lock.Packs, "auth")
	root := t.TempDir()
	out, err := plan(t.Context(), gen.Input{Root: root, Lock: lock, Args: []string{"admin"}, Flags: flags(t, "--server", "--role", "manager,staff")})
	if err != nil {
		t.Fatal(err)
	}
	server, _ := out.Plan.Get("web/src/routes/admin/+page.server.ts")
	if got := string(server.Data); !strings.Contains(got, "import { requireRole } from '#lib/server/auth.js';") || !strings.Contains(got, "requireRole(locals, url, 'manager', 'staff');") {
		t.Fatalf("server page =\n%s", got)
	}
	for _, args := range [][]string{{"--role", "manager"}, {"--server", "--role", "other"}, {"--server", "--role", ""}} {
		_, err := plan(t.Context(), gen.Input{Root: root, Lock: lock, Args: []string{"admin"}, Flags: flags(t, args...)})
		if err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("flags %v error = %v", args, err)
		}
	}
}

func TestRoleWithoutLoadDiagnostics(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ mode, want string }{
		{"node", "cannot generate a role-guarded page without --server, because only a server load can guard a page\nTry: add --server"},
		{"embed", "cannot generate a role-guarded page without a load function\nTry: add --server"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			lock := webLock()
			lock.Answers["frontend_mode"] = tc.mode
			lock.Packs = append(lock.Packs, "auth")
			_, err := plan(t.Context(), gen.Input{Root: t.TempDir(), Lock: lock, Args: []string{"reports"}, Flags: flags(t, "--role", "manager")})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
