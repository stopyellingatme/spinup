package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestConsoleFlags(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		args []string
		code int
		want []string
	}{
		{[]string{"console", "--help"}, 0, []string{"open", "spinup console open --env production"}},
		{[]string{"console", "open", "--help"}, 0, []string{"--no-browser", "--accept-new-host-key", "--env", "--dir"}},
		{[]string{"status", "--help"}, 0, []string{"--json", "--accept-new-host-key", "--env", "spinup status --json --env production"}},
		{[]string{"console", "open", "extra"}, ui.ExitUsage, []string{"Try: spinup console open --help"}},
		{[]string{"status", "extra"}, ui.ExitUsage, []string{"Try: spinup status --help"}},
		{[]string{"status", "--no-browser"}, ui.ExitUsage, []string{"unknown flag: --no-browser"}},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()
			code, out, stderr := run(t, tt.args...)
			if code != tt.code {
				t.Fatalf("code=%d out=%s err=%s", code, out, stderr)
			}
			for _, want := range tt.want {
				if !strings.Contains(out+stderr, want) {
					t.Errorf("output misses %q: %s%s", want, out, stderr)
				}
			}
		})
	}
}

// consoleProject writes a project and puts a fake ssh first on PATH. The fake ssh fails like
// an unreachable VM, so the commands never reach a real host.
func consoleProject(t *testing.T, modules string) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "c")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	bin := filepath.Join(home, "bin")
	if err = os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	ssh := "#!/bin/sh\necho 'ssh: connect to host vm port 22: Connection refused' >&2\nexit 255\n"
	if err = os.WriteFile(filepath.Join(bin, "ssh"), []byte(ssh), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("SPINUP_SSH_CONFIG", "")
	t.Setenv("SPINUP_BROWSER", "false")
	dir := t.TempDir()
	toml := "modules=[" + modules + "]\n[app]\nname='todo'\nmodule='example.com/todo'\n[env.production]\nhost='vm'\nuser='deploy'\n"
	if err = os.WriteFile(filepath.Join(dir, "spinup.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// oneError checks that stderr holds exactly one JSON error document, on its own line.
// Diagnostic lines before it are plain text.
func oneError(t *testing.T, stderr string) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, line := range strings.Split(stderr, "\n") {
		var doc struct {
			Error map[string]any `json:"error"`
		}
		if json.Unmarshal([]byte(line), &doc) == nil && doc.Error != nil {
			found = append(found, doc.Error)
		}
	}
	if len(found) != 1 {
		t.Fatalf("stderr has %d JSON errors, want one: %s", len(found), stderr)
	}
	return found[0]
}

func TestConsoleStatusCLIDisabledModule(t *testing.T) {
	dir := consoleProject(t, "'deploy'")
	code, out, stderr := run(t, "status", "--json", "-C", dir, "--env", "production")
	if code != ui.ExitError || out != "" {
		t.Fatalf("code=%d stdout=%q stderr=%s", code, out, stderr)
	}
	e := oneError(t, stderr)
	if e["what"] != "Cannot open console: module console is disabled." || e["try"] != "spinup module add console" {
		t.Fatalf("error = %v", e)
	}
	code, out, stderr = run(t, "console", "open", "-C", dir, "--env", "production", "--no-browser")
	if code != ui.ExitError || out != "" || !strings.Contains(stderr, "Cannot open console: module console is disabled.") || !strings.Contains(stderr, "Try: spinup module add console") {
		t.Fatalf("code=%d stdout=%q stderr=%s", code, out, stderr)
	}
}

func TestConsoleStatusCLITunnelFailure(t *testing.T) {
	dir := consoleProject(t, "'deploy','console'")
	code, out, stderr := run(t, "status", "--json", "-C", dir, "--env", "production", "--accept-new-host-key")
	if code != ui.ExitError || out != "" {
		t.Fatalf("code=%d stdout=%q stderr=%s", code, out, stderr)
	}
	e := oneError(t, stderr)
	if e["what"] != "Cannot read console: the service for production did not answer." || e["try"] != "spinup doctor --remote --env production" {
		t.Fatalf("error = %v", e)
	}
	if !strings.Contains(stderr, "Connection refused") {
		t.Fatalf("stderr misses the SSH cause: %s", stderr)
	}
}

func TestConsoleOpenCLITunnelFailure(t *testing.T) {
	dir := consoleProject(t, "'deploy','console'")
	code, out, stderr := run(t, "console", "open", "-C", dir, "--env", "production")
	if code != ui.ExitError || out != "" {
		t.Fatalf("code=%d stdout=%q stderr=%s", code, out, stderr)
	}
	for _, want := range []string{"Cannot read console: the service for production did not answer.", "Try: spinup doctor --remote --env production", "Connection refused"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr misses %q: %s", want, stderr)
		}
	}
}
