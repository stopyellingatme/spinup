package templates_test

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

// jobsGoldenPath selects the files of the jobs pack and the API main file,
// which registers the River migration hook when the jobs module is on.
func jobsGoldenPath(path string) bool {
	return strings.HasPrefix(path, "api/cmd/worker/") ||
		strings.HasPrefix(path, "api/internal/jobs/") ||
		path == "api/cmd/api/main.go"
}

func TestGoldenJobs(t *testing.T) {
	t.Parallel()
	testutil.GoldenSelected(t, "jobs")
	answers := map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": "ci,jobs"}
	render := func() (fs.FS, error) {
		all := testutil.RenderPack(t, []string{"base", "api", "jobs"}, answers)
		selected := fstest.MapFS{}
		err := fs.WalkDir(all, ".", func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !jobsGoldenPath(path) {
				return err
			}
			data, err := fs.ReadFile(all, path)
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			selected[path] = &fstest.MapFile{Data: data, Mode: info.Mode()}
			return nil
		})
		return selected, err
	}
	fsys, err := render()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "jobs", fsys)
	testutil.Deterministic(t, render)

	main, err := fs.ReadFile(fsys, "api/cmd/api/main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"db.WithHook(jobs.MigrationHook())", `app.WithCommand("jobs-migrate"`} {
		if !bytes.Contains(main, []byte(want)) {
			t.Errorf("api/cmd/api/main.go does not contain %s", want)
		}
	}
}

// TestJobsOffKeepsAPIMain checks that an app without the jobs module gets
// the API main file of the api pack alone, byte for byte.
func TestJobsOffKeepsAPIMain(t *testing.T) {
	t.Parallel()
	base := map[string]any{"app_name": "todo", "module": "example.com/todo"}
	without, err := fs.ReadFile(testutil.RenderPack(t, []string{"base", "api"}, base), "api/cmd/api/main.go")
	if err != nil {
		t.Fatal(err)
	}
	withCI := map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": "ci"}
	ci, err := fs.ReadFile(testutil.RenderPack(t, []string{"base", "api", "ci"}, withCI), "api/cmd/api/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(without, ci) {
		t.Fatal("api/cmd/api/main.go changes with the ci module")
	}
	if bytes.Contains(without, []byte("jobs")) {
		t.Fatal("api/cmd/api/main.go mentions jobs without the jobs module")
	}
}
