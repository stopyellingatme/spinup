package resource

import (
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen"
)

func TestKitMigrationReaders(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, map[string]string{
		"api/db/kit/00007_tables.sql":        "-- +goose Up\nCREATE TABLE users (id bigint PRIMARY KEY, email text NOT NULL);\nCREATE TABLE projects (id bigint PRIMARY KEY, user_id bigint NOT NULL REFERENCES users(id));\nCREATE UNIQUE INDEX projects_owner ON projects(user_id, id);",
		"api/db/migrations/00001_tables.sql": "-- +goose Up\nCREATE TABLE users (id bigint PRIMARY KEY, wrong text NOT NULL);",
	})
	def, ok, err := findTable(root, "users")
	if err != nil || !ok || !def.Kit || !strings.Contains(def.Body, "email") {
		t.Fatalf("findTable = %+v, %v, %v", def, ok, err)
	}
	key, err := hasOwnerKey(root, "projects", "")
	if err != nil || !key {
		t.Fatalf("hasOwnerKey = %v, %v", key, err)
	}
	spec, err := ParseSpec(root, []string{"note", "user:ref:user"}, "")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanModel(spec, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan.Paths(), ","), "api/db/migrations/00008_create_notes.sql") {
		t.Fatal(plan.Paths())
	}
}

func TestGeneratedResourceKitMigrations(t *testing.T) {
	t.Parallel()
	for _, authenticated := range []bool{false, true} {
		t.Run(map[bool]string{false: "without auth", true: "with auth"}[authenticated], func(t *testing.T) {
			t.Parallel()
			packs := []string{"base", "api"}
			var modules []string
			if authenticated {
				packs = append(packs, "auth")
				modules = []string{"auth"}
			}
			root := renderApp(t, packs, modules)
			out, err := run(t.Context(), gen.Input{Root: root, Args: []string{"note", "title:string"}, Flags: flagSet(t, "--api-only")})
			if err != nil {
				t.Fatal(err)
			}
			var source string
			for _, file := range out.Plan.Files {
				if file.Path == "api/internal/handlers/notes_test.go" {
					source = string(file.Data)
				}
			}
			if source == "" {
				t.Fatal("test file missing")
			}
			option := "testkit.WithKitMigrations(kitmigrations.FS)"
			if strings.Contains(source, option) != authenticated {
				t.Fatal(source)
			}
			if strings.Contains(source, `kitmigrations "example.com/todo/api/db/kit"`) != authenticated {
				t.Fatal(source)
			}
		})
	}
}

func TestKitAuditMigrationOrder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, map[string]string{"api/db/kit/00006_audit_events.sql": "-- +goose Up\nCREATE TABLE audit_events(id bigint);"})
	for _, name := range []string{"00002_create_notes.sql", "00006_create_notes.sql", "00007_create_notes.sql"} {
		if err := CheckAuditMigration(root, name); err != nil {
			t.Errorf("kit schema must precede %s: %v", name, err)
		}
	}
}
