package page

import (
	"context"
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
