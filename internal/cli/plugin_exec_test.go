package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/manifest"
)

func TestPluginExecTrustFlags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, tc := range []struct {
		name     string
		flags    []string
		approved bool
	}{
		{"trust alone", []string{"--trust"}, false},
		{"wrong name", []string{"--trust-exec", "other"}, false},
		{"typed name", []string{"--trust-exec", "hello"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "plugins", "hello")
			if err := os.MkdirAll(filepath.Join(source, "bin"), 0755); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{
				"spinup.toml":               "[app]\nname='demo'\nmodule='example.com/demo'\n",
				"plugins/hello/plugin.toml": "name='hello'\nversion='1.0.0'\napi=1\nspinup='*'\nkind='exec'\n[exec]\nbinaries={'" + runtime.GOOS + "-" + runtime.GOARCH + "'='bin/hello'}\n",
				"plugins/hello/pack.toml":   "name='hello'\n",
				"plugins/hello/bin/hello":   "unused executable",
			}
			for path, data := range files {
				if err := os.WriteFile(filepath.Join(root, path), []byte(data), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if err := manifest.New().Save(filepath.Join(root, "spinup.lock")); err != nil {
				t.Fatal(err)
			}
			code, _, stderr := run(t, append([]string{"plugin", "add", "path:./plugins/hello", "--dir", root}, tc.flags...)...)
			if (code == 0) != tc.approved {
				t.Fatalf("add code=%d stderr=%s", code, stderr)
			}
			if !tc.approved {
				if !strings.Contains(stderr, "--trust-exec hello") {
					t.Fatal(stderr)
				}
				return
			}
			code, _, stderr = run(t, "plugin", "update", "hello", "--dir", root, "--trust")
			if code == 0 || !strings.Contains(stderr, "--trust-exec hello") {
				t.Fatalf("update accepted --trust: code=%d stderr=%s", code, stderr)
			}
			code, _, stderr = run(t, "plugin", "update", "hello", "--dir", root, "--trust-exec", "hello")
			if code != 0 {
				t.Fatalf("update code=%d stderr=%s", code, stderr)
			}
			data, err := os.ReadFile(filepath.Join(root, ".spinup/plugins.allow"))
			if err != nil || !strings.HasSuffix(string(data), " exec\n") {
				t.Fatalf("allowlist=%s err=%v", data, err)
			}
		})
	}
}
