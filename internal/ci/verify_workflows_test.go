package ci_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ci"
	spexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/testutil"
	"go.yaml.in/yaml/v3"
)

func TestVerifyRenderedPack(t *testing.T) {
	t.Parallel()
	for _, modules := range []string{"ci", "ci,web,deploy,backups"} {
		t.Run(modules, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			tree := testutil.RenderPack(t, []string{"ci"}, map[string]any{"app_name": "todo", "modules": modules})
			if err := fs.WalkDir(tree, ".", func(name string, entry fs.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				data, err := fs.ReadFile(tree, name)
				if err != nil {
					return err
				}
				file := filepath.Join(root, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
					return err
				}
				return os.WriteFile(file, data, 0644)
			}); err != nil {
				t.Fatal(err)
			}
			report, err := ci.Verify(context.Background(), ci.VerifyOptions{Root: root, LookPath: func(string) (string, error) { return "", &spexec.NotFoundError{Name: "actionlint"} }})
			if err != nil || len(report.Findings) != 0 {
				t.Fatalf("report %v, error %v", report, err)
			}
		})
	}
}

func TestMaintenanceWorkflows(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	pins := stackversions.MustLoad()
	for _, name := range []string{"bump", "shas", "canary-pins", "kit-compat"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(filepath.Join(root, ".github/workflows", name+".yml"))
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Permissions map[string]string `yaml:"permissions"`
				Jobs        map[string]struct {
					Timeout int                          `yaml:"timeout-minutes"`
					Steps   []struct{ Uses, Run string } `yaml:"steps"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc.Permissions) != 1 || doc.Permissions["contents"] != "read" {
				t.Fatalf("permissions: %v", doc.Permissions)
			}
			for jobName, job := range doc.Jobs {
				if job.Timeout <= 0 {
					t.Errorf("job %s has no timeout", jobName)
				}
				for _, step := range job.Steps {
					if step.Uses != "" {
						action, sha, _ := strings.Cut(step.Uses, "@")
						if pins.Actions[action].SHA != sha {
							t.Errorf("action %s is not pinned by the stack table", step.Uses)
						}
					}
				}
			}
		})
	}
}

func TestMaintenanceGoHelpersCompile(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	for _, name := range []string{"bump", "shas"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(filepath.Join(root, ".github/workflows", name+".yml"))
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Jobs map[string]struct{ Steps []struct{ Run string } }
			}
			if err := yaml.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, job := range doc.Jobs {
				for _, step := range job.Steps {
					_, helper, ok := strings.Cut(step.Run, "<<'GO'\n")
					if !ok {
						continue
					}
					helper, _, ok = strings.Cut(helper, "\nGO\n")
					if !ok {
						t.Fatal("missing helper terminator")
					}
					found = true
					dir := t.TempDir()
					file := filepath.Join(dir, "main.go")
					if err := os.WriteFile(file, []byte(helper), 0600); err != nil {
						t.Fatal(err)
					}
					_, err := (spexec.OSRunner{}).Run(context.Background(), spexec.Cmd{Name: "go", Args: []string{"build", "-o", filepath.Join(dir, "helper"), file}, Dir: root, Env: []string{"GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "GOPROXY=off"}})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if !found {
				t.Fatal("no Go helper")
			}
		})
	}
}

func TestKitCompatCorpusSelection(t *testing.T) {
	t.Parallel()
	python, err := spexec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), ".github/workflows/kit-compat.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Jobs map[string]struct{ Steps []struct{ Run string } }
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	script := ""
	for _, job := range doc.Jobs {
		for _, step := range job.Steps {
			if _, tail, ok := strings.Cut(step.Run, "<<'PY'\n"); ok {
				script, _, _ = strings.Cut(tail, "\nPY\n")
			}
		}
	}
	if script == "" {
		t.Fatal("missing corpus selection script")
	}
	for _, tc := range []struct {
		name     string
		versions []string
		want     string
	}{
		{"synthetic", []string{"0.0.0-dev"}, "0.0.0-dev"},
		{"stable", []string{"0.0.0-dev", "0.9.0", "0.10.0"}, "0.10.0"},
		{"release beats prerelease", []string{"0.3.0-rc.10", "0.3.0"}, "0.3.0"},
		{"numeric prerelease", []string{"0.3.0-rc.2", "0.3.0-rc.10"}, "0.3.0-rc.10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			runner := filepath.Join(root, "runner")
			if err := os.MkdirAll(runner, 0755); err != nil {
				t.Fatal(err)
			}
			for _, version := range tc.versions {
				tree := filepath.Join(root, "testdata/corpus", version, "default/tree")
				if version == "0.0.0-dev" {
					tree = filepath.Join(root, "testdata/corpus", version, "api/tree")
				}
				writeCIFile(t, filepath.Join(tree, "spinup.lock"), `{"template_version":"`+version+`"}`, 0644)
			}
			summary := filepath.Join(root, "summary")
			if _, err := (spexec.OSRunner{}).Run(context.Background(), spexec.Cmd{Name: python, Args: []string{"-c", script}, Dir: root, Env: []string{"RUNNER_TEMP=" + runner, "GITHUB_STEP_SUMMARY=" + summary}}); err != nil {
				t.Fatal(err)
			}
			lock, err := os.ReadFile(filepath.Join(runner, "kit-compat/spinup.lock"))
			if err != nil {
				t.Fatal(err)
			}
			if string(lock) != `{"template_version":"`+tc.want+`"}` {
				t.Fatalf("selected %s, want %s", lock, tc.want)
			}
		})
	}
}
