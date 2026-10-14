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
	testscript.Run(t, testscript.Params{
		Files: files,
		Setup: func(env *testscript.Env) error {
			for _, key := range []string{"GOTOOLCHAIN", "GOFLAGS", "GOPROXY", "GOCACHE", "GOMODCACHE", "PATH"} {
				if value, ok := os.LookupEnv(key); ok {
					env.Setenv(key, value)
				}
			}
			env.Setenv("PATH", binDir+string(os.PathListSeparator)+env.Getenv("PATH"))
			env.Setenv("HOME", env.WorkDir)
			env.Setenv("SPINUP_NO_UPDATE_CHECK", "1")
			env.Setenv("NO_COLOR", "1")
			return nil
		},
	})
}
