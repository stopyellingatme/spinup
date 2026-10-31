package upgrade

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/project"
)

const roleMigrationPath = "api/db/migrations/00005_users_role.sql"

func TestAuthUpgradeWarnings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, config, migration, want string
		rolesExist, noAuth            bool
	}{
		{name: "old auth app"},
		{name: "app migration", migration: "00005_widgets.sql", want: "Try: rename your migration to the next free number, then run spinup upgrade again."},
		{name: "other version", migration: "00004_widgets.sql"},
		{name: "same migration", migration: "00005_users_role.sql"},
		{name: "custom auth", config: "[auth]\nroles = ['owner', 'cook']\nadmin_role = 'owner'\ndefault_role = 'cook'\n", want: "Try: run spinup g roles after the upgrade."},
		{name: "empty auth", config: "[auth]\n", want: "Try: run spinup g roles after the upgrade."},
		{name: "comment only", config: "# [auth]\n"},
		{name: "existing roles", config: "[auth]\n", rolesExist: true},
		{name: "no auth pack", config: "[auth]\n", migration: "00005_widgets.sql", noAuth: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if tc.config != "" {
				writePlanFile(t, root, project.ConfigFile, tc.config, 0o644)
			}
			if tc.migration != "" {
				writePlanFile(t, root, "api/db/migrations/"+tc.migration, "-- app migration\n", 0o644)
			}
			if tc.rolesExist {
				writePlanFile(t, root, project.RolesFilePath, "roles\n", 0o644)
			}
			specs := []planSpec{{path: roleMigrationPath, data: "-- roles\n", class: "seed"}, {path: project.RolesFilePath, data: "roles\n", class: "managed"}}
			if tc.noAuth {
				specs = nil
			}
			plan, err := BuildPlan(t.Context(), PlanInput{Root: root, Lock: planLock("", ""), Old: planVersion("0.1.0"), New: planVersion("0.2.0", specs...)})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := plan.Format(&out); err != nil {
				t.Fatal(err)
			}
			if tc.want != "" && !strings.Contains(out.String(), tc.want) {
				t.Fatalf("missing %q in:\n%s", tc.want, &out)
			}
			if tc.want == "" && strings.Contains(out.String(), "Try:") {
				t.Fatalf("unexpected warning:\n%s", &out)
			}
			if _, err := os.Stat(root + "/" + project.RolesFilePath); !tc.rolesExist && !os.IsNotExist(err) {
				t.Fatal("planner wrote roles file")
			}
		})
	}
}

func TestAuthUpgradeWarningErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path, data, want string
		directory              bool
	}{
		{name: "invalid configuration", path: project.ConfigFile, data: "[auth", want: "correct spinup.toml"},
		{name: "configuration directory", path: project.ConfigFile, directory: true, want: "check permissions on spinup.toml"},
		{name: "migration directory is a file", path: "api/db/migrations", data: "not a directory", want: "check permissions on api/db/migrations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if tc.directory {
				if err := os.Mkdir(root+"/"+tc.path, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				writePlanFile(t, root, tc.path, tc.data, 0o644)
			}
			dir, err := os.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer dir.Close()
			plan := &Plan{Files: []FileDecision{{Path: roleMigrationPath, Action: ActionCreate}, {Path: project.RolesFilePath, Action: ActionCreate}}}
			err = authUpgradeWarnings(dir, plan)
			if err == nil || !strings.Contains(err.Error(), "\nTry: "+tc.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestAuthUpgradeWarningOrder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{"00005_z.sql", "00005_a.sql"} {
		writePlanFile(t, root, "api/db/migrations/"+name, "-- app migration", 0o644)
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	plan := &Plan{Files: []FileDecision{{Path: roleMigrationPath, Action: ActionCreate}}}
	if err := authUpgradeWarnings(dir, plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Files[0].Reason, "00005_a.sql, 00005_z.sql") {
		t.Fatal(plan.Files[0].Reason)
	}
}
