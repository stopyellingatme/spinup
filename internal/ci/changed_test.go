package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ci"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.CommandContext(t.Context(), "git", full...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestMatrixChanged(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	const all = `{"include":[{"name":"alpha","lang":"rust"},{"name":"beta","lang":"rust"}]}`
	for _, tc := range []struct {
		name    string
		changed string
		ref     string
		want    string
		warn    bool
	}{
		{"one service", "services/alpha/src/main.rs", "base", `{"include":[{"name":"alpha","lang":"rust"}]}`, false},
		{"other service", "services/beta/Cargo.toml", "base", `{"include":[{"name":"beta","lang":"rust"}]}`, false},
		{"no service", "api/main.go", "base", `{"include":[]}`, false},
		{"spinup.toml", "spinup.toml", "base", all, false},
		{"workflow", ".github/workflows/ci.yml", "base", all, false},
		{"action", ".github/actions/setup-spinup/action.yml", "base", all, false},
		{"spinup.lock", "spinup.lock", "base", all, false},
		{"non-ASCII name", "services/alpha/données.py", "base", `{"include":[{"name":"alpha","lang":"rust"}]}`, false},
		{"go.mod", "go.mod", "base", all, false},
		{"api go.mod", "api/go.mod", "base", all, false},
		{"compose file", "compose.services.yml", "base", all, false},
		{"bad ref", "services/alpha/x", "missing-ref", all, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := serviceProject(t, serviceTable("alpha", 8101)+serviceTable("beta", 8102))
			for _, name := range []string{"alpha/Cargo.toml", "beta/Cargo.toml"} {
				writeServiceFile(t, filepath.Join(root, "services", name), "")
			}
			gitIn(t, root, "init", "-q", "-b", "main")
			gitIn(t, root, "add", "-A")
			gitIn(t, root, "commit", "-q", "-m", "base")
			gitIn(t, root, "branch", "base")
			target := filepath.Join(root, filepath.FromSlash(tc.changed))
			old, _ := os.ReadFile(target)
			writeServiceFile(t, target, string(old)+"\n# changed\n")
			gitIn(t, root, "add", "-A")
			gitIn(t, root, "commit", "-q", "-m", "change")
			var errOut bytes.Buffer
			doc, err := ci.MatrixChanged(context.Background(), nil, root, tc.ref, &errOut)
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tc.want {
				t.Fatalf("matrix = %s, want %s", data, tc.want)
			}
			if got := strings.Contains(errOut.String(), "Try:"); got != tc.warn {
				t.Fatalf("warning = %q, want warning %v", errOut.String(), tc.warn)
			}
		})
	}
}
