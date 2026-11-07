package modules

import (
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/templates"
)

func TestSelectAudit(t *testing.T) {
	t.Parallel()
	lock := manifest.New()
	lock.Packs = []string{"base", "api", "web"}
	lock.Answers = map[string]any{"app_name": "demo", "module": "example.com/demo", "db_version": "18"}
	got, err := Select(t.Context(), SelectOptions{Config: &project.Config{}, Lock: lock, FS: templates.Embedded(), Names: []string{"audit"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Added, ",") != "audit,auth" || strings.Join(got.Required, ",") != "auth" {
		t.Fatalf("selection = %+v", got)
	}
	lock.Packs = []string{"base", "api"}
	_, err = Select(t.Context(), SelectOptions{Config: &project.Config{}, Lock: lock, FS: templates.Embedded(), Names: []string{"audit"}})
	if err == nil || !strings.Contains(err.Error(), "API-only") {
		t.Fatalf("error = %v", err)
	}
}
