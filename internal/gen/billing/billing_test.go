package billing

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
)

func TestPlan(t *testing.T) {
	root := t.TempDir()
	lock := manifest.New()
	if _, err := plan(context.Background(), gen.Input{Root: root, Lock: lock}); err == nil || !strings.Contains(err.Error(), "Try: spinup module add billing") {
		t.Fatalf("error = %v", err)
	}
	lock.Packs = []string{"billing"}
	config := filepath.Join(root, project.ConfigFile)
	if err := os.WriteFile(config, []byte("[billing]\n[[billing.plans]]\nname='free'\n[[billing.plans]]\nname='pro'\nprices=['pro_monthly']\nfeatures=['reports']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := plan(context.Background(), gen.Input{Root: root, Lock: lock})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Plan.Files) != 2 || len(out.Edits) != 2 {
		t.Fatalf("output = %+v", out)
	}
	for i, path := range []string{project.BillingFilePath, project.BillingTSFilePath} {
		f := out.Plan.Files[i]
		if f.Path != path || f.Class != string(manifest.Managed) || !strings.Contains(string(f.Data), "reports") {
			t.Fatalf("file = %+v", f)
		}
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatalf("plan wrote %s", path)
		}
	}
	if err := os.WriteFile(config, []byte("[billing]\n[[billing.plans]]\nname='free'\n[[billing.plans]]\nname='bad'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := plan(context.Background(), gen.Input{Root: root, Lock: lock}); err == nil || !strings.Contains(err.Error(), "must have 1 to 8 prices") {
		t.Fatalf("error = %v", err)
	}
	if _, err := plan(context.Background(), gen.Input{Args: []string{"extra"}}); err == nil || !strings.Contains(err.Error(), "Try: spinup g billing") {
		t.Fatalf("error = %v", err)
	}
}
