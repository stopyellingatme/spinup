package component

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func lock(packs []string, answers map[string]any) *manifest.Lock {
	l := manifest.New()
	l.Packs = packs
	l.Answers = answers
	return l
}

var web = []string{"base", "api", "web"}

func TestPlan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		answers map[string]any
		want    string
	}{
		{"lock answer", map[string]any{"shadcn_registry": "1.8.0"}, "shadcn-svelte@1.8.0"},
		{"default", nil, "shadcn-svelte@1.7.0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := plan(context.Background(), gen.Input{Lock: lock(web, tc.answers), Args: []string{"dialog", "dropdown-menu"}})
			if err != nil {
				t.Fatal(err)
			}
			want := []gen.Command{{Dir: "web", Argv: []string{"pnpm", "dlx", tc.want, "add", "dialog", "dropdown-menu", "--yes", "--no-deps-install"}}}
			if !reflect.DeepEqual(out.Commands, want) {
				t.Fatalf("commands %+v", out.Commands)
			}
			// The files are tool output: the plan is empty, so the lock records nothing.
			if len(out.Plan.Files) != 0 || len(out.PostSteps) != 0 {
				t.Fatalf("output %+v", out)
			}
		})
	}
}

func TestPlanRefusals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   gen.Input
		want string
	}{
		{"no names", gen.Input{Lock: lock(web, nil)}, "at least one component name"},
		{"bad name", gen.Input{Lock: lock(web, nil), Args: []string{"dialog", "Dialog"}}, `component name "Dialog" is invalid`},
		{"flag-like name", gen.Input{Lock: lock(web, nil), Args: []string{"--all"}}, "is invalid"},
		{"no web", gen.Input{Lock: lock([]string{"base", "api"}, nil), Args: []string{"dialog"}}, "no web pack"},
		{"bad registry", gen.Input{Lock: lock(web, map[string]any{"shadcn_registry": "latest"}), Args: []string{"dialog"}}, "not a version"},
		{"registry type", gen.Input{Lock: lock(web, map[string]any{"shadcn_registry": 1}), Args: []string{"dialog"}}, "not a version"},
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
