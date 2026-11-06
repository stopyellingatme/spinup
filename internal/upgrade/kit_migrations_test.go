package upgrade

import (
	"bytes"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"

	execx "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/merge"
	"github.com/stopyellingatme/spinup/internal/migrate"
)

func TestPlanDropsMissingRemovedFiles(t *testing.T) {
	t.Parallel()
	for _, class := range []string{"seed", "managed", "scaffold"} {
		t.Run(class, func(t *testing.T) {
			root := t.TempDir()
			lock := planLock("old", class)
			newer := planVersion("0.2.0")
			plan, err := BuildPlan(t.Context(), PlanInput{Root: root, Lock: lock, New: newer})
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Files) != 1 || plan.Files[0].Action != ActionDelete {
				t.Fatalf("plan = %+v", plan.Files)
			}
			if err := writePlan(root, plan); err != nil {
				t.Fatal(err)
			}
			if len(plan.Tombstoned) != 0 {
				t.Fatalf("removed files became tombstones: %v", plan.Tombstoned)
			}
			next, err := NextLock(lock, plan, newer, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := next.Files["file.txt"]; ok {
				t.Fatal("dead lock entry remains")
			}
		})
	}
}

func TestKitMigrationsPlanner(t *testing.T) {
	t.Parallel()
	base := filepath.Join("..", "..", "testdata", "corpus", "kit-migrations")
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(base)); err != nil {
		t.Fatal(err)
	}
	oldSpecs := auditCorpusSpecs(t, root)
	old := planVersion("0.1.0-alpha.419", oldSpecs...)
	lock := planLock("", "")
	lock.TemplateVersion = old.Version
	rendered, err := Render(old, lock.Packs, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range rendered.Files {
		entry := manifest.FileEntry{Class: manifest.Class(file.Class)}
		if entry.Class != manifest.Seed {
			entry.Template, entry.Hash, entry.Mode = file.Template, manifest.Hash(file.Data), "0644"
		}
		lock.Files[file.Path] = entry
	}
	var specs []planSpec
	for _, spec := range oldSpecs {
		if spec.path == "api/db/migrations/00002_create_auth.sql" {
			spec.path = "api/db/kit/00002_create_auth.sql"
		}
		specs = append(specs, spec)
	}
	specs = append(specs, planSpec{path: "api/db/kit/embed.go", data: "package kit\n", class: "managed"})
	newer := planVersion("0.1.0-alpha.420", specs...)
	r := &Runner{Root: root, Source: chainSource{old.Version: old, newer.Version: newer}, Out: &bytes.Buffer{}, Merger: merge.NewGit(execx.OSRunner{})}
	report, err := r.preview(t.Context(), lock, []string{newer.Version}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	plan := report.Steps[0]
	want := map[string]Action{
		"api/db/migrations/00002_create_auth.sql":  ActionDelete,
		"api/db/kit/00002_create_auth.sql":         ActionKeep,
		"api/db/kit/embed.go":                      ActionCreate,
		"api/db/migrations/00001_init.sql":         ActionKeep,
		"api/db/migrations/00003_create_todos.sql": ActionKeep,
	}
	for _, file := range plan.Files {
		if expected, ok := want[file.Path]; ok && file.Action != expected {
			t.Errorf("%s = %s, want %s", file.Path, file.Action, expected)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "api/db/migrations/00002_create_auth.sql")); err != nil {
		t.Fatal("preview changed source", err)
	}
	if len(lock.AppliedMigrations) != 0 {
		t.Fatal("preview changed lock")
	}
	for _, m := range migrate.All() {
		if m.ID == migrate.KitMigrationsID {
			if err := m.Apply(&migrate.Ctx{Root: root}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writePlan(root, plan); err != nil {
		t.Fatal(err)
	}
	next, err := NextLock(lock, plan, newer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := next.Files["api/db/migrations/00002_create_auth.sql"]; ok {
		t.Fatal("dead lock entry remains")
	}
	if next.Files["api/db/kit/00002_create_auth.sql"].Class != manifest.Seed {
		t.Fatal("moved seed is not tracked")
	}
}

func TestPlanCopiesKitMigrationPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writePlanFile(t, root, "api/db/kit/00002_create_auth.sql", "kit", 0o644)
	writePlanFile(t, root, "api/db/migrations/00003_create_todos.sql", "app", 0o644)
	paths := map[string]bool{}
	if err := addPlanMigrationPaths(root, paths); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"api/db/kit/00002_create_auth.sql", "api/db/migrations/00003_create_todos.sql"} {
		if !paths[path] {
			t.Errorf("missing %s", path)
		}
	}
}

func TestKitMigrationsMainMerge(t *testing.T) {
	t.Parallel()
	const mainPath = "api/cmd/api/main.go"
	const oldMain = `package main
import (
 "example.com/demo/api/db/migrations"
 "github.com/stopyellingatme/spinup/kit/db"
)
var migrate = db.MigrateCommand(migrations.FS, db.WithDatabaseURL(cfg.DatabaseURL))
`
	const newMain = `package main
import (
 kitmigrations "example.com/demo/api/db/kit"
 "example.com/demo/api/db/migrations"
 "github.com/stopyellingatme/spinup/kit/db"
)
var migrate = db.MigrateCommand(migrations.FS, db.WithDatabaseURL(cfg.DatabaseURL), db.WithKitMigrations(kitmigrations.FS))
`
	for _, edited := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged main", true: "custom migrate call"}[edited], func(t *testing.T) {
			root := t.TempDir()
			formatted, err := format.Source([]byte(oldMain))
			if err != nil {
				t.Fatal(err)
			}
			disk := string(formatted)
			if edited {
				disk = strings.Replace(disk, "db.WithDatabaseURL(cfg.DatabaseURL)", "db.WithDatabaseURL(customURL)", 1)
			}
			writePlanFile(t, root, mainPath, disk, 0o644)
			writePlanFile(t, root, "api/sqlc.yaml", "schema: db/migrations\n", 0o644)
			lock := planLock("", "")
			old := planVersion("0.1.0-alpha.419", planSpec{path: mainPath, data: oldMain, class: "scaffold"}, planSpec{path: "api/sqlc.yaml", data: "schema: db/migrations\n", class: "managed"})
			rendered, err := Render(old, lock.Packs, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range rendered.Files {
				lock.Files[file.Path] = manifest.FileEntry{Class: manifest.Class(file.Class), Template: file.Template, Hash: manifest.Hash(file.Data), Mode: "0644"}
			}
			newer := planVersion("0.1.0-alpha.420", planSpec{path: mainPath, data: newMain, class: "scaffold"}, planSpec{path: "api/sqlc.yaml", data: "schema: [db/kit, db/migrations]\n", class: "managed"})
			plan, err := BuildPlan(t.Context(), PlanInput{Root: root, Lock: lock, Old: old, New: newer, Merger: merge.NewGit(execx.OSRunner{})})
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range plan.Files {
				want := ActionOverwrite
				if file.Path == mainPath && edited {
					want = ActionConflict
				}
				if file.Action != want {
					t.Errorf("%s = %s, want %s", file.Path, file.Action, want)
				}
			}
		})
	}
}
