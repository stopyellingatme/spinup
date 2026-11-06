package modules

import (
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/templates"
)

func TestSelectFlags(t *testing.T) {
	t.Parallel()
	lock := manifest.New()
	lock.Packs = []string{"base", "api", "web"}
	lock.Answers = map[string]any{"app_name": "demo", "module": "example.com/demo", "db_version": "18"}
	got, err := Select(t.Context(), SelectOptions{Config: &project.Config{}, Lock: lock, FS: templates.Embedded(), Names: []string{"flags"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Added, ",") != "auth,flags" || strings.Join(got.Required, ",") != "auth" {
		t.Fatalf("selection=%+v", got)
	}
	lock.Packs = []string{"base", "api"}
	if _, err := Select(t.Context(), SelectOptions{Config: &project.Config{}, Lock: lock, FS: templates.Embedded(), Names: []string{"flags"}}); err == nil || !strings.Contains(err.Error(), "API-only") {
		t.Fatalf("error=%v", err)
	}
}

func TestFlagsNextSteps(t *testing.T) {
	t.Parallel()
	for _, want := range []string{
		"Next: declare a flag in spinup.toml, for example:\n [flags.new_checkout]\n description = \"The new checkout flow\"\n public = true\n",
		`appflags "<module>/api/internal/flags"`,
		"flags.NewStore(pool, flagsProvider, roles.Set), and call flagStore.Start(ctx, refresh). See docs/build/flags.md.",
		`app.WithMiddleware(flagsProvider.Middleware()),`,
		`app.WithRoutes(flagStore.Routes(authn)),`,
		`app.WithCommand("flags", flagStore.Command()),`,
		`app.WithReadiness("flags", flagStore.Ready),`,
		`app.WithShutdown(flagStore.Close),`,
		"In web/src/app.d.ts add flags: Record<string, boolean> to Locals.",
		"Next: run spinup doctor and inspect the flags-drift and flags-wired checks.\n",
	} {
		if !strings.Contains(FlagsNextSteps(), want) {
			t.Errorf("missing %q", want)
		}
	}
}
