package roles

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/run"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
)

func TestPlanCustomRoles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeConfig(t, root, "[auth]\nroles = [\"owner\", \"front_desk\"]\nadmin_role = \"owner\"\ndefault_role = \"front_desk\"\n")

	out, err := plan(context.Background(), gen.Input{Root: root, Lock: authLock()})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := out.Edits, []string{project.RolesFilePath}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Edits = %v, want %v", got, want)
	}
	if len(out.Plan.Files) != 1 || out.Plan.Files[0].Path != project.RolesFilePath {
		t.Fatalf("plan = %+v", out.Plan)
	}
	if got, want := string(out.Plan.Files[0].Data), string(project.RenderRolesFile(project.AuthRoles{Names: []string{"owner", "front_desk"}, Admin: "owner", Default: "front_desk"})); got != want {
		t.Fatalf("roles file = %q, want %q", got, want)
	}
}

func TestPlanRefusals(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		lock   *manifest.Lock
		config string
		want   string
	}{
		{name: "no auth", lock: manifest.New(), config: "", want: "no auth module"},
		{name: "missing config", lock: authLock(), config: "", want: "spinup.toml is missing"},
		{name: "invalid roles", lock: authLock(), config: "[auth]\nroles = []\n", want: "list length"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if tt.config != "" {
				writeConfig(t, root, tt.config)
			}
			_, err := plan(context.Background(), gen.Input{Root: root, Lock: tt.lock})
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestRunDoesNothingForCurrentFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeConfig(t, root, "")
	lock := authLock()
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	writeRolesFile(t, root, project.RenderRolesFile((&project.Config{}).AuthRoles()))

	var output strings.Builder
	result, err := run.Run(context.Background(), gen.Generator{Name: "roles", Run: plan}, nil, nil, run.Options{Root: root, Out: &output})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].Action != "unchanged" {
		t.Fatalf("files = %+v", result.Files)
	}
	if got := output.String(); !strings.Contains(got, "Created 0, updated 0 files") {
		t.Fatalf("output = %q", got)
	}
}

func authLock() *manifest.Lock {
	lock := manifest.New()
	lock.Packs = []string{"base", "api", "auth"}
	return lock
}

func writeConfig(t *testing.T, root, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, project.ConfigFile), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeRolesFile(t *testing.T, root string, data []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(project.RolesFilePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
