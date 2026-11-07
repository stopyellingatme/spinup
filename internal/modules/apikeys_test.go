package modules

import (
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/templates"
)

func TestSelectAPIKeys(t *testing.T) {
	t.Parallel()
	lock := manifest.New()
	lock.Packs = []string{"base", "api", "web"}
	lock.Answers = map[string]any{"app_name": "demo", "module": "example.com/demo", "db_version": "18"}
	got, err := Select(t.Context(), SelectOptions{Config: &project.Config{}, Lock: lock, FS: templates.Embedded(), Names: []string{"apikeys"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Added, ",") != "apikeys,auth" || strings.Join(got.Required, ",") != "auth" {
		t.Fatalf("selection=%+v", got)
	}
	lock.Packs = []string{"base", "api"}
	if _, err := Select(t.Context(), SelectOptions{Config: &project.Config{}, Lock: lock, FS: templates.Embedded(), Names: []string{"apikeys"}}); err == nil || !strings.Contains(err.Error(), "API-only") {
		t.Fatalf("error=%v", err)
	}
}

func TestAPIKeysNextSteps(t *testing.T) {
	for _, want := range []string{"[apikeys.plans.free]", "spinup g apikeys", "auth.WithBearer(keyStore)", "app.WithShutdown(keyStore.Close)", "/settings/api-keys", "/admin/apikeys", "spinup db migrate", "apikeys-drift", "apikeys-wired"} {
		if !strings.Contains(APIKeysNextSteps(), want) {
			t.Errorf("missing %q", want)
		}
	}
}
