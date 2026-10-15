package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestModuleFlags(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"add"}, {"add", "jobs", "--continue"}, {"add", "--continue", "--abort"}, {"list", "extra"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			code, _, stderr := run(t, append([]string{"module"}, args...)...)
			if code != ui.ExitUsage || !strings.Contains(stderr, "Try:") {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
		})
	}
}

func TestModuleList(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte("modules = ['ci','jobs']\n[app]\nname='demo'\nmodule='example.com/demo'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run(t, "module", "list", "--dir", root, "--json")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	var rows []struct {
		Name        string
		Enabled     bool
		Requires    []string
		Description string
	}
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.Name == "jobs" {
			found = row.Enabled && len(row.Requires) > 0 && row.Description != ""
		}
		if row.Name == "web" {
			t.Fatal("web is not a module")
		}
	}
	if !found {
		t.Fatalf("list=%s", stdout)
	}
	code, stdout, stderr = run(t, "module", "list", "--dir", root)
	if code != 0 || !strings.Contains(stdout, "STATE") || !strings.Contains(stdout, "monitoring") {
		t.Fatalf("list=%s error=%s", stdout, stderr)
	}
}

func TestModuleAddHelp(t *testing.T) {
	t.Parallel()
	code, stdout, stderr := run(t, "module", "add", "--help")
	if code != 0 {
		t.Fatal(stderr)
	}
	for _, flag := range []string{"--continue", "--abort", "--monitoring-profile", "--ci-deploy-on-push", "--yes", "--non-interactive"} {
		if !strings.Contains(stdout, flag) {
			t.Errorf("missing %s", flag)
		}
	}
}
