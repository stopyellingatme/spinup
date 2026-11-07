package apikeysdrift

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
)

func TestDrift(t *testing.T) {
	root := t.TempDir()
	c := &doctor.Context{Root: root}
	if got := runCheck(context.Background(), c); got.Status != doctor.Skip {
		t.Fatal(got)
	}
	lock := manifest.New()
	if err := lock.Save(filepath.Join(root, project.LockFile)); err != nil {
		t.Fatal(err)
	}
	if got := runCheck(context.Background(), c); got.Status != doctor.Skip {
		t.Fatal(got)
	}
	lock.Packs = []string{"apikeys"}
	if err := lock.Save(filepath.Join(root, project.LockFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, project.ConfigFile), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got := runCheck(context.Background(), c); got.Status != doctor.Warn || got.Try != "run spinup g apikeys" {
		t.Fatal(got)
	}
	cfg := (&project.Config{}).APIKeysConfig()
	for _, f := range []struct {
		path string
		data []byte
	}{{project.APIKeysFilePath, project.RenderAPIKeysFile(cfg)}, {project.APIKeysTSFilePath, project.RenderAPIKeysTSFile(cfg, (&project.Config{}).AuthRoles())}} {
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
	if err := os.WriteFile(filepath.Join(root, project.ConfigFile), []byte("[apikeys]\nprefix='!'"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := runCheck(context.Background(), c); got.Status != doctor.Warn {
		t.Fatal(got)
	}
}
