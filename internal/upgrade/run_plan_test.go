package upgrade

import (
	"context"
	"testing"
)

func TestPlanSeparatesUntrackedMigrationNumbers(t *testing.T) {
	t.Parallel()
	r, root := runFixture(t, false)
	r.Source.(chainSource)["0.2.0"] = planVersion("0.2.0",
		planSpec{path: "file.txt", data: "new\n"},
		planSpec{path: roleMigrationPath, data: "-- roles\n", class: "seed"},
	)
	writePlanFile(t, root, "api/db/migrations/00005_widgets.sql", "-- app migration\n", 0o644)

	report, err := r.Plan(context.Background(), Options{To: "0.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(report.Steps))
	}
	for _, file := range report.Steps[0].Files {
		if file.Path == roleMigrationPath {
			if file.Action != ActionCreate || file.Reason != "" {
				t.Fatalf("kit migration = %+v", file)
			}
			return
		}
	}
	t.Fatalf("kit migration is missing: %+v", report.Steps[0].Files)
}
