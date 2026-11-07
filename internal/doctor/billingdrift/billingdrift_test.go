package billingdrift

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
)

func TestDrift(t *testing.T) {
	root := t.TempDir()
	c := &doctor.Context{Root: root}
	lock := manifest.New()
	if err := lock.Save(filepath.Join(root, project.LockFile)); err != nil {
		t.Fatal(err)
	}
	if got := runCheck(context.Background(), c); got.Status != doctor.Skip {
		t.Fatal(got)
	}
	lock.Packs = []string{"billing"}
	if err := lock.Save(filepath.Join(root, project.LockFile)); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, project.ConfigFile)
	if err := os.WriteFile(config, nil, 0600); err != nil {
		t.Fatal(err)
	}
	got := runCheck(context.Background(), c)
	if got.Status != doctor.Warn || got.Try != "run spinup g billing" || !strings.Contains(got.Message, project.BillingFilePath) || !strings.Contains(got.Message, project.BillingTSFilePath) {
		t.Fatal(got)
	}
	d := (&project.Config{}).BillingConfig()
	for _, f := range []struct {
		path string
		data []byte
	}{{project.BillingFilePath, project.RenderBillingFile(d)}, {project.BillingTSFilePath, project.RenderBillingTSFile(d)}} {
		p := filepath.Join(root, f.path)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if got := runCheck(context.Background(), c); got.Status != doctor.OK {
		t.Fatal(got)
	}
	if err := os.WriteFile(config, []byte("[billing]\ntrial_days=731\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := runCheck(context.Background(), c); got.Status != doctor.Warn || !strings.Contains(got.Message, "invalid billing") {
		t.Fatal(got)
	}
}
