package migrate_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen/resource/validation"
	"github.com/stopyellingatme/spinup/internal/migrate"
	"github.com/stopyellingatme/spinup/internal/migrate/migratetest"
)

func validationMigration(t *testing.T) migrate.Migration {
	t.Helper()
	for _, m := range migrate.All() {
		if m.ID == "0002_validation_known_fields" {
			return m
		}
	}
	t.Fatal("validation migration is not registered")
	return migrate.Migration{}
}

func TestValidationKnownFieldsMigration(t *testing.T) {
	t.Parallel()
	m := validationMigration(t)
	if m.Stage != migrate.After || m.Since != "0.1.0-alpha.419" {
		t.Fatalf("migration stage = %d, since = %s", m.Stage, m.Since)
	}
	pending, err := migrate.Pending("0.1.0-alpha.418", "0.1.0-alpha.419", []string{migrate.KitTestkitID}, migrate.After)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != m.ID {
		t.Fatalf("pending = %+v", pending)
	}
	before := writeTree(t, map[string]string{validation.Path: validation.LegacySource, "api/internal/handlers/notes.go": "package handlers\nfunc init() { requiredFields[\"POST /api/notes\"] = []string{\"title\"} }\n"})
	after := writeTree(t, map[string]string{validation.Path: validation.Source, "api/internal/handlers/notes.go": "package handlers\nfunc init() { requiredFields[\"POST /api/notes\"] = []string{\"title\"} }\n"})
	mtime := filepath.Join(before, filepath.FromSlash(validation.Path))
	if err := os.Chmod(mtime, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(after, filepath.FromSlash(validation.Path)), 0o600); err != nil {
		t.Fatal(err)
	}
	changes, err := m.Plan(&migrate.Ctx{Root: before})
	if err != nil || len(changes) != 1 || changes[0].Path != validation.Path {
		t.Fatalf("plan = %+v, error = %v", changes, err)
	}
	migratetest.Run(t, m, before, after)
}

func TestValidationKnownFieldsNoop(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		files map[string]string
	}{
		{"absent", nil},
		{"current", map[string]string{validation.Path: validation.Source}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := writeTree(t, tc.files)
			m := validationMigration(t)
			changes, err := m.Plan(&migrate.Ctx{Root: root})
			if err != nil || len(changes) != 0 {
				t.Fatalf("plan = %+v, error = %v", changes, err)
			}
			migratetest.Run(t, m, root, root)
		})
	}
}

func TestValidationKnownFieldsManualStep(t *testing.T) {
	t.Parallel()
	m := validationMigration(t)
	source := validation.LegacySource + "// User customization.\n"
	root := writeTree(t, map[string]string{validation.Path: source})
	c := &migrate.Ctx{Root: root}
	changes, err := m.Plan(c)
	if err == nil || len(changes) != 1 || !strings.Contains(changes[0].Desc, "Manual step:") {
		t.Fatalf("plan = %+v, error = %v", changes, err)
	}
	applied, err := migrate.Run(c, []migrate.Migration{m})
	if err == nil || len(applied) != 0 {
		t.Fatalf("applied = %v, error = %v", applied, err)
	}
	for _, want := range []string{validation.Path, "differ", "Try:", "var knownFields = map[string][]string{}", "httpx.MaxBody(maxJSONBody)", "httpx.CheckJSONBody"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %s, want %s", err, want)
		}
	}
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(validation.Path)))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != source {
		t.Fatal("migration changed customized validation")
	}
}

func TestValidationKnownFieldsChangedAfterPlan(t *testing.T) {
	t.Parallel()
	m := validationMigration(t)
	root := writeTree(t, map[string]string{validation.Path: validation.LegacySource})
	c := &migrate.Ctx{Root: root}
	if _, err := m.Plan(c); err != nil {
		t.Fatal(err)
	}
	source := validation.LegacySource + "// Changed after planning.\n"
	path := filepath.Join(root, filepath.FromSlash(validation.Path))
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.Apply(c); err == nil {
		t.Fatal("migration overwrote an edit after planning")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != source {
		t.Fatal("migration changed customized validation")
	}
}

func TestValidationKnownFieldsNonregular(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(validation.Path)), 0o755); err != nil {
		t.Fatal(err)
	}
	m := validationMigration(t)
	c := &migrate.Ctx{Root: root}
	for _, check := range []func() error{
		func() error { _, err := m.Plan(c); return err },
		func() error { return m.Apply(c) },
	} {
		if err := check(); err == nil || !strings.Contains(err.Error(), "not a regular file") || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("error = %v, want a regular-file error", err)
		}
	}
}
