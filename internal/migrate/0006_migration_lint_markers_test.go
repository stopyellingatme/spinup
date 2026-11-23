package migrate_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/db/lint"
	"github.com/stopyellingatme/spinup/internal/migrate"
	"github.com/stopyellingatme/spinup/internal/templates"
)

func TestMigrationLintMarkers(t *testing.T) {
	paths := []string{"auth/files/api/db/kit/00004_oidc_identities.sql", "audit/files/api/db/kit/00006_audit_events.sql", "flags/files/api/db/kit/00008_feature_flags.sql", "apikeys/files/api/db/kit/00009_api_keys.sql", "billing/files/api/db/kit/00010_billing.sql"}
	old, want := map[string]string{}, map[string]string{}
	for _, path := range paths {
		data, err := fs.ReadFile(templates.Embedded(), path)
		if err != nil {
			t.Fatal(err)
		}
		name := "api/db/kit/" + filepath.Base(path)
		var lines []string
		for _, line := range strings.SplitAfter(string(data), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "-- spinup:allow-destructive") {
				lines = append(lines, line)
			}
		}
		old[name], want[name] = strings.Join(lines, ""), string(data)
	}
	root := writeTree(t, old)
	m := kitMigration(t, "0006_migration_lint_markers")
	pending, err := migrate.Pending("0.1.0-alpha.903", "0.1.0-alpha.904", nil, migrate.After)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range pending {
		if item.ID == m.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("codemod is not selected for the release")
	}
	c := &migrate.Ctx{Root: root}
	changes, err := m.Plan(c)
	if err != nil || len(changes) != len(paths) {
		t.Fatalf("plan = %v, %v", changes, err)
	}
	if err := m.Apply(c); err != nil {
		t.Fatal(err)
	}
	for name, expected := range want {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(got) != expected {
			t.Fatalf("%s: unexpected content: %v", name, err)
		}
		strip := func(s string) string {
			var result []string
			for _, line := range strings.SplitAfter(s, "\n") {
				if !strings.HasPrefix(strings.TrimSpace(line), "--") {
					result = append(result, line)
				}
			}
			return strings.Join(result, "")
		}
		if strip(old[name]) != strip(string(got)) {
			t.Fatalf("%s: SQL changed", name)
		}
	}
	findings, err := lint.LintDir(filepath.Join(root, "api/db/kit"))
	if err != nil || len(findings) != 0 {
		t.Fatalf("lint = %v, %v", findings, err)
	}
	if err := m.Apply(c); err != nil {
		t.Fatal(err)
	}
	changes, err = m.Plan(c)
	if err != nil || len(changes) != 0 {
		t.Fatalf("second plan = %v, %v", changes, err)
	}
	name := "api/db/kit/00006_audit_events.sql"
	changed := []byte(old[name] + "-- custom\n")
	if err := os.WriteFile(filepath.Join(root, name), changed, 0600); err != nil {
		t.Fatal(err)
	}
	changes, err = m.Plan(c)
	if err != nil || len(changes) != 1 || !strings.Contains(changes[0].Desc, "-- spinup:allow-destructive adds audit triggers only when absent") {
		t.Fatalf("manual plan = %v, %v", changes, err)
	}
	if err := m.Apply(c); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, name))
	if err != nil || !bytes.Equal(got, changed) {
		t.Fatalf("custom file changed: %v", err)
	}
}
