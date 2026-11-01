package modules

import (
	"strings"
	"testing"
	"testing/fstest"

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

func TestMigrationClashes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		files     []string
		additions []string
		want      map[string]string
	}{
		{name: "todo", files: []string{"00001_init.sql", "00002_create_auth.sql", "00003_create_todos.sql"}, additions: []string{"00005_users_role.sql", "00006_audit_events.sql"}},
		{name: "all versions", files: []string{"00003_todos.sql", "00004_notes.sql", "00005_widgets.sql", "00006_reports.sql"}, additions: []string{"00003_password_resets.sql", "00004_oidc_identities.sql", "00005_users_role.sql", "00006_audit_events.sql"}, want: map[string]string{"00003_password_resets.sql": "00003_todos.sql", "00004_oidc_identities.sql": "00004_notes.sql", "00005_users_role.sql": "00005_widgets.sql", "00006_audit_events.sql": "00006_reports.sql"}},
		{name: "numeric and sorted", files: []string{"6_z.sql", "00006_a.sql", "junk.sql"}, additions: []string{"00006_audit_events.sql"}, want: map[string]string{"00006_audit_events.sql": "00006_a.sql, 6_z.sql"}},
		{name: "same path", files: []string{"00006_audit_events.sql"}, additions: []string{"00006_audit_events.sql"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			disk := fstest.MapFS{}
			for _, name := range tc.files {
				disk["api/db/migrations/"+name] = &fstest.MapFile{}
			}
			paths := make([]string, 0, len(tc.additions))
			for _, name := range tc.additions {
				paths = append(paths, "api/db/migrations/"+name)
			}
			got, err := MigrationClashes(disk, paths)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("warnings = %v", got)
			}
			for name, collision := range tc.want {
				warning := got["api/db/migrations/"+name]
				if !strings.Contains(warning, collision) || !strings.Contains(warning, name) || !strings.Contains(warning, "\nTry:") || !strings.Contains(warning, "next free number") {
					t.Fatalf("warning = %q", warning)
				}
			}
		})
	}
}
