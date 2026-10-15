package upgrade

import (
	"testing"

	"github.com/stopyellingatme/spinup/internal/manifest"
)

func TestPlanKeepsPluginGeneratorOwnedFiles(t *testing.T) {
	t.Parallel()
	for _, namespace := range []string{"gen", "@gen", "@exec"} {
		for _, edited := range []bool{false, true} {
			name := namespace + "/unmodified"
			if edited {
				name = namespace + "/edited"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				data := "generated"
				if edited {
					data = "user edits"
				}
				writePlanFile(t, root, "file.txt", data, 0644)
				lock := planLock("generated", "managed")
				entry := lock.Files["file.txt"]
				entry.Template = "plugin/example/" + namespace + "/greeting"
				lock.Files["file.txt"] = entry
				newer := planVersion("0.2.0")
				plan, err := BuildPlan(t.Context(), PlanInput{Root: root, Lock: lock, Old: planVersion("0.1.0"), New: newer})
				if err != nil {
					t.Fatal(err)
				}
				if len(plan.Files) != 1 || plan.Files[0].Action != ActionKeep || plan.Files[0].Reason != "generator-owned" {
					t.Fatalf("decisions=%+v, want keep generator-owned", plan.Files)
				}
				next, err := NextLock(lock, plan, newer, nil)
				if err != nil {
					t.Fatal(err)
				}
				if next.Files["file.txt"] != entry || len(next.Orphans) != 0 {
					t.Fatalf("entry=%+v orphans=%v, want original entry without orphans", next.Files["file.txt"], next.Orphans)
				}
				if next.Files["file.txt"].Hash != manifest.Hash([]byte("generated")) {
					t.Fatal("generator hash changed")
				}
			})
		}
	}
}
