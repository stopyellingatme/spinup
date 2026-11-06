package migrate_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/migrate"
	"github.com/stopyellingatme/spinup/internal/migrate/migratetest"
)

func kitMigration(t *testing.T, id string) migrate.Migration {
	t.Helper()
	for _, m := range migrate.All() {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("migration %s is not registered", id)
	return migrate.Migration{}
}

func TestKitMigrationsMove(t *testing.T) {
	t.Parallel()
	m := kitMigrationWithPacks(t, "auth", "audit")
	if m.Stage != migrate.Before || m.Since != "0.1.0-alpha.419" {
		t.Fatalf("migration = %+v", m)
	}
	files := map[string]string{
		"api/db/migrations/00001_init.sql":         "-- init\n",
		"api/db/migrations/00003_create_todos.sql": "-- app\n",
		"api/db/migrations/embed.go":               "package migrations\n",
	}
	afterFiles := map[string]string{}
	for name, data := range files {
		afterFiles[name] = data
	}
	names := []string{"00002_create_auth.sql", "00003_password_resets.sql", "00004_oidc_identities.sql", "00005_users_role.sql", "00006_audit_events.sql", "00007_users_auth_version.sql"}
	for _, name := range names {
		files["api/db/migrations/"+name] = "-- customized\r\n" + name
		afterFiles["api/db/kit/"+name] = files["api/db/migrations/"+name]
	}
	before, after := writeTree(t, files), writeTree(t, afterFiles)
	if err := os.Chmod(filepath.Join(before, "api/db/migrations/00002_create_auth.sql"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(after, "api/db/kit/00002_create_auth.sql"), 0o600); err != nil {
		t.Fatal(err)
	}
	changes, err := m.Plan(&migrate.Ctx{Root: before})
	if err != nil || len(changes) != len(names) {
		t.Fatalf("changes = %+v, error = %v", changes, err)
	}
	for i, name := range names {
		if changes[i].Desc != "Move "+name+" to api/db/kit" {
			t.Fatal(changes[i])
		}
	}
	migratetest.Run(t, m, before, after)
}

func TestKitMigrationsRenamed(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"00008_users_role.sql", "00009_create_auth.sql", "00010_password_resets.sql", "00011_oidc_identities.sql", "00012_audit_events.sql", "00013_users_auth_version.sql"} {
		t.Run(name, func(t *testing.T) {
			root := writeTree(t, map[string]string{"api/db/migrations/" + name: "-- renamed", "api/db/migrations/00002_create_auth.sql": "-- original"})
			m := kitMigrationWithPacks(t, "auth", "audit")
			c := &migrate.Ctx{Root: root}
			changes, err := m.Plan(c)
			if err == nil || len(changes) == 0 || changes[len(changes)-1].Desc != "Manual step: "+name+" is a renamed kit migration" {
				t.Fatalf("changes = %+v, error = %v", changes, err)
			}
			if !strings.Contains(err.Error(), "Try: move the file to api/db/kit under its own number, run UPDATE goose_db_version SET version_id = ") || !strings.Contains(err.Error(), "once in every database") {
				t.Fatal(err)
			}
			if err := m.Apply(c); err == nil {
				t.Fatal("applied renamed migration")
			}
			if _, err := os.Stat(filepath.Join(root, "api/db/migrations/00002_create_auth.sql")); err != nil {
				t.Fatal("partially moved files", err)
			}
		})
	}
}

func TestKitMigrationsCopies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, target string
		fail         bool
	}{
		{"equal", "-- source\r\n", false}, {"different", "-- edited\n", true}, {"line endings", "-- source\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeTree(t, map[string]string{"api/db/migrations/00002_create_auth.sql": "-- source\r\n", "api/db/kit/00002_create_auth.sql": tc.target})
			m := kitMigrationWithPacks(t, "auth", "audit")
			err := m.Apply(&migrate.Ctx{Root: root})
			if tc.fail {
				if err == nil || !strings.Contains(err.Error(), "Try: remove one of the two copies") {
					t.Fatal(err)
				}
				data, err := os.ReadFile(filepath.Join(root, "api/db/migrations/00002_create_auth.sql"))
				if err != nil || !bytes.Equal(data, []byte("-- source\r\n")) {
					t.Fatalf("source = %q, error = %v", data, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err := m.Apply(&migrate.Ctx{Root: root}); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(root, "api/db/migrations/00002_create_auth.sql")); !os.IsNotExist(err) {
					t.Fatal("old copy remains", err)
				}
			}
			data, err := os.ReadFile(filepath.Join(root, "api/db/kit/00002_create_auth.sql"))
			if err != nil || string(data) != tc.target {
				t.Fatalf("destination = %q, error = %v", data, err)
			}
		})
	}
}

func TestKitMigrationsNoop(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	m := kitMigration(t, "0003_kit_migrations")
	changes, err := m.Plan(&migrate.Ctx{Root: root})
	if err != nil || len(changes) != 0 {
		t.Fatalf("changes = %+v, error = %v", changes, err)
	}
	migratetest.Run(t, m, root, root)
}

func kitMigrationWithPacks(t *testing.T, packs ...string) migrate.Migration {
	t.Helper()
	m := kitMigration(t, migrate.KitMigrationsID)
	plan, apply := m.Plan, m.Apply
	m.Plan = func(c *migrate.Ctx) ([]migrate.Change, error) {
		copy := *c
		copy.Lock = &manifest.Lock{Packs: packs}
		return plan(&copy)
	}
	m.Apply = func(c *migrate.Ctx) error {
		copy := *c
		copy.Lock = &manifest.Lock{Packs: packs}
		return apply(&copy)
	}
	return m
}

func TestKitMigrationsPackOwnership(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		packs []string
		file  string
		move  bool
	}{
		{name: "app password resets without auth", packs: []string{"api"}, file: "00003_password_resets.sql"},
		{name: "renamed app file without auth", packs: []string{"api"}, file: "00009_create_auth.sql"},
		{name: "audit name without audit", packs: []string{"api", "auth"}, file: "00006_audit_events.sql"},
		{name: "renamed audit name without audit", packs: []string{"api", "auth"}, file: "00009_audit_events.sql"},
		{name: "auth file", packs: []string{"api", "auth"}, file: "00003_password_resets.sql", move: true},
		{name: "audit file", packs: []string{"api", "auth", "audit"}, file: "00006_audit_events.sql", move: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := "api/db/migrations/" + tc.file
			root := writeTree(t, map[string]string{path: "-- app bytes\n"})
			m := kitMigrationWithPacks(t, tc.packs...)
			c := &migrate.Ctx{Root: root}
			changes, err := m.Plan(c)
			wantCount := 0
			if tc.move {
				wantCount = 1
			}
			if err != nil || len(changes) != wantCount {
				t.Fatalf("changes = %v, error = %v", changes, err)
			}
			if err := m.Apply(c); err != nil {
				t.Fatal(err)
			}
			if tc.move {
				path = "api/db/kit/" + tc.file
			}
			data, err := os.ReadFile(filepath.Join(root, path))
			if err != nil || string(data) != "-- app bytes\n" {
				t.Fatalf("file = %q, error = %v", data, err)
			}
		})
	}
}

func TestKitCodemodsSelectedForRelease(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		id    string
		stage migrate.Stage
	}{
		{migrate.KitMigrationsID, migrate.Before}, {migrate.KitTestkitID, migrate.After},
	} {
		pending, err := migrate.Pending("0.1.0-alpha.418", "0.1.0-alpha.419", nil, tc.stage)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, m := range pending {
			if m.ID == tc.id {
				found = true
			}
		}
		if !found {
			t.Errorf("%s not selected for alpha.419", tc.id)
		}
	}
}
