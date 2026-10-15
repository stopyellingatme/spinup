package scripttest

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestScripts(t *testing.T) {
	root := testutil.RepoRoot(t)
	candidates, err := filepath.Glob(filepath.Join(root, "testdata", "script", "*.txtar"))
	if err != nil {
		t.Fatal(err)
	}
	pattern := os.Getenv("SPINUP_SCRIPT_RUN")
	if _, err := path.Match(pattern, ""); err != nil {
		t.Fatalf("select scripts: %v\nTry: use a valid SPINUP_SCRIPT_RUN glob", err)
	}
	var files []string
	for _, file := range candidates {
		name := strings.TrimSuffix(filepath.Base(file), ".txtar")
		if strings.HasPrefix(name, "app_gate_") && os.Getenv("SPINUP_APP_GATE") != "1" {
			continue
		}
		if pattern != "" {
			matches, err := path.Match(pattern, name)
			if err != nil {
				t.Fatal(err)
			}
			if !matches {
				continue
			}
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		t.Skipf("no scripts match SPINUP_SCRIPT_RUN=%q (app gates require SPINUP_APP_GATE=1)", pattern)
	}
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "spinup")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	// Building this test binary is the permitted os/exec exception.
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", binary, filepath.Join(root, "cmd", "spinup"))
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build spinup: %v\n%s\nTry: correct the build failure", err, output)
	}
	// Scripts get their own HOME, so pin the Go caches to the real ones. Otherwise an
	// app-gate build would start with an empty module cache and fail offline.
	goEnv := exec.CommandContext(t.Context(), "go", "env", "GOCACHE", "GOMODCACHE")
	goEnv.Dir = root
	out, err := goEnv.Output()
	if err != nil {
		t.Fatalf("go env: %v\nTry: check the Go installation", err)
	}
	caches := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(caches) != 2 {
		t.Fatalf("go env printed %q, want two lines", out)
	}
	testscript.Run(t, testscript.Params{
		Files: files,
		Setup: func(env *testscript.Env) error {
			env.Setenv("GOCACHE", caches[0])
			env.Setenv("GOMODCACHE", caches[1])
			for _, key := range []string{"GOTOOLCHAIN", "GOFLAGS", "GOPROXY", "PATH", "DATABASE_URL", "CI"} {
				if value, ok := os.LookupEnv(key); ok {
					env.Setenv(key, value)
				}
			}
			// SPINUP_REPO lets app-gate scripts point SPINUP_KIT_REPLACE at $SPINUP_REPO/kit.
			env.Setenv("SPINUP_REPO", root)
			env.Setenv("PATH", binDir+string(os.PathListSeparator)+env.Getenv("PATH"))
			env.Setenv("HOME", env.WorkDir)
			env.Setenv("SPINUP_NO_UPDATE_CHECK", "1")
			env.Setenv("NO_COLOR", "1")
			return nil
		},
	})
}
