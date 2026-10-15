package ci_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/ci"
	"github.com/stopyellingatme/spinup/internal/gen/newapp"
	genrun "github.com/stopyellingatme/spinup/internal/gen/run"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/templates/store"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

type ciSource struct {
	version *templates.Version
	loaded  string
}

func (s *ciSource) Versions(context.Context) ([]string, error) {
	return []string{s.version.Version}, nil
}
func (s *ciSource) Load(_ context.Context, version string) (*templates.Version, error) {
	s.loaded = version
	return s.version, nil
}

func TestGenerate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                                  string
		edit, remove, stale, force, dry, diff bool
		conflict                              bool
	}{
		{name: "no-op"}, {name: "missing", remove: true}, {name: "update", stale: true},
		{name: "conflict", edit: true, conflict: true}, {name: "force", edit: true, force: true},
		{name: "preview", remove: true, dry: true}, {name: "diff", remove: true, diff: true},
		{name: "preview conflict", edit: true, dry: true, conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			result, err := newapp.Generate(ctx, newapp.Options{Name: "todo", Dir: t.TempDir(), APIOnly: true, NoGit: true, Version: "dev", Answers: map[string]any{"module": "example.com/todo", "modules": "ci"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(result.Dir, 0755); err != nil {
				t.Fatal(err)
			}
			if _, err := vfs.Apply(result.Dir, result.Plan, vfs.ApplyOptions{}); err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(result.Dir, "spinup.lock")
			if err := result.Lock.Save(lockPath); err != nil {
				t.Fatal(err)
			}
			const name = ".github/workflows/ci.yml"
			file := filepath.Join(result.Dir, filepath.FromSlash(name))
			original, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			source := &ciSource{version: store.Current("dev")}
			if tc.edit || tc.stale {
				writeCIFile(t, file, "edited\n", 0644)
			}
			if tc.stale {
				entry := result.Lock.Files[name]
				entry.Hash = manifest.Hash([]byte("edited\n"))
				result.Lock.Files[name] = entry
				if err := result.Lock.Save(lockPath); err != nil {
					t.Fatal(err)
				}
			}
			if tc.remove {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			_, err = ci.Generate(ctx, source, genrun.Options{Root: result.Dir, Force: tc.force, DryRun: tc.dry, Diff: tc.diff, Out: &out})
			if tc.conflict {
				if err == nil || !strings.Contains(err.Error(), "--force") {
					t.Fatalf("error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if source.loaded != result.Lock.TemplateVersion {
				t.Fatalf("loaded %q", source.loaded)
			}
			after, err := os.ReadFile(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			if tc.dry || tc.diff || tc.name == "no-op" {
				if !bytes.Equal(before, after) {
					t.Fatal("preview or no-op changed lock")
				}
			}
			if tc.dry || tc.diff {
				if _, err := os.Stat(file); !os.IsNotExist(err) {
					t.Fatal("preview wrote file")
				}
				return
			}
			actual, err := os.ReadFile(file)
			if err != nil || !bytes.Equal(actual, original) {
				t.Fatalf("file: %s, %v", actual, err)
			}
			lock, err := manifest.Load(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			if lock.Files[name].Template != result.Lock.Files[name].Template || lock.Files[name].Hash != manifest.Hash(original) {
				t.Fatalf("entry: %v", lock.Files[name])
			}
			// Regeneration must not give later user edits generator-owned overwrite rights.
			writeCIFile(t, file, "another edit\n", 0644)
			if _, err := ci.Generate(ctx, source, genrun.Options{Root: result.Dir}); err == nil {
				t.Fatal("second edit did not conflict")
			}
		})
	}
}

func TestGenerateUsesLockedInputs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	lock := manifest.New()
	lock.CLIVersion = "dev"
	lock.TemplateVersion = "1.2.3"
	lock.MinCLI = "0.0.0-dev"
	lock.Kit = manifest.KitRef{Module: "github.com/stopyellingatme/spinup/kit", Version: "v0.1.0"}
	lock.BehaviorVersion = 1
	lock.FormatterGo = "go1.27.2"
	lock.Packs = []string{"base", "api", "ci"}
	lock.Answers = map[string]any{"app_name": "locked-app"}
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	source := &ciSource{version: &templates.Version{Version: "1.2.3", Stack: store.Current("dev").Stack, Packs: fstest.MapFS{
		"ci/pack.toml": {Data: []byte(`name = "ci"
[[questions]]
key = "app_name"
type = "string"
[[files]]
path = ".github/workflows/locked.yml"
source = "locked.yml.tmpl"
class = "managed"
`)},
		"ci/locked.yml.tmpl": {Data: []byte("name: [[ .app_name ]] [[ .TemplateVersion ]]\n")},
	}}}
	if _, err := ci.Generate(ctx, source, genrun.Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, ".github/workflows/locked.yml"))
	if err != nil || string(got) != "name: locked-app 1.2.3\n" {
		t.Fatalf("output %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".github/workflows/ci.yml")); !os.IsNotExist(err) {
		t.Fatal("rendered current templates instead of the locked source")
	}
	after, err := manifest.Load(filepath.Join(root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if after.Files[".github/workflows/locked.yml"].Template != "ci/locked.yml.tmpl" {
		t.Fatalf("lock: %v", after.Files)
	}
}
