package flags

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/run"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
)

func TestPlan(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, project.ConfigFile), []byte("[auth]\nroles = [\"owner\"]\nadmin_role = \"owner\"\ndefault_role = \"owner\"\n[flags.demo]\ndescription = \"Demo\"\npublic = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := plan(context.Background(), gen.Input{Root: root, Lock: manifest.New()}); err == nil || err.Error() != "cannot generate flags: the app has no flags module\nTry: spinup module add flags" {
		t.Fatalf("error = %v", err)
	}
	lock := manifest.New()
	lock.Packs = []string{"flags"}
	out, err := plan(context.Background(), gen.Input{Root: root, Lock: lock})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Edits) != 2 || len(out.Plan.Files) != 2 || !strings.Contains(string(out.Plan.Files[1].Data), "adminRole = 'owner'") {
		t.Fatalf("output = %+v", out)
	}
}

func TestRunUnchanged(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, project.ConfigFile), nil, 0600); err != nil {
		t.Fatal(err)
	}
	lock := manifest.New()
	lock.Packs = []string{"flags"}
	if err := lock.Save(filepath.Join(root, project.LockFile)); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		path string
		data []byte
	}{{project.FlagsFilePath, project.RenderFlagsFile(nil)}, {project.FlagsTSFilePath, project.RenderFlagsTSFile(nil, (&project.Config{}).AuthRoles())}} {
		p := filepath.Join(root, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var output strings.Builder
	result, err := run.Run(context.Background(), gen.Generator{Name: "flags", Run: plan}, nil, nil, run.Options{Root: root, Out: &output})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 2 || result.Files[0].Action != "unchanged" || result.Files[1].Action != "unchanged" || !strings.Contains(output.String(), "Created 0, updated 0 files") {
		t.Fatalf("result = %+v, output = %s", result, output.String())
	}
}

func TestPositionalArgument(t *testing.T) {
	_, err := plan(context.Background(), gen.Input{Args: []string{"extra"}})
	want := "cannot generate flags: expected no positional arguments\nTry: spinup g flags"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestUnknownFieldKeepsTryLine(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, project.ConfigFile), []byte("[flags.demo]\ndescription = \"Demo\"\nbogus = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock := manifest.New()
	lock.Packs = []string{"flags"}
	_, err := plan(context.Background(), gen.Input{Root: root, Lock: lock})
	if err == nil || !strings.Contains(err.Error(), "Try: correct [flags.demo] in spinup.toml") || strings.Contains(err.Error(), "inside a spinup project") {
		t.Fatalf("err = %v", err)
	}
}
