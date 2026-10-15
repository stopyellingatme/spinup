package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/drift"
	execx "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestDoctorExecPlugins(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "plugins", "hello")
	if err := os.MkdirAll(filepath.Join(source, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (execx.OSRunner{}).Run(t.Context(), execx.Cmd{Name: "go", Args: []string{"build", "-o", filepath.Join(source, "bin", "hello"), "./internal/plugin/rpc/testdata"}, Dir: repo}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	files := map[string]string{
		"spinup.toml":               "[app]\nname='demo'\nmodule='example.com/demo'\n",
		"plugins/hello/plugin.toml": "name='hello'\nversion='1'\napi=1\nspinup='*'\nkind='exec'\n[exec]\nbinaries={'" + runtime.GOOS + "-" + runtime.GOARCH + "'='bin/hello'}\n",
		"plugins/hello/pack.toml":   "name='hello'\n",
	}
	for path, data := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := manifest.New().Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := run(t, "plugin", "add", "path:./plugins/hello", "--dir", root, "--trust-exec", "hello"); code != ui.ExitOK {
		t.Fatalf("plugin add: code=%d stderr=%s", code, stderr)
	}
	subdir := filepath.Join(root, "nested")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ behavior, status, dir string }{
		{"success", "warn", root},
		{"check-fail", "fail", subdir},
	} {
		t.Run(tc.status, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(root, "behavior"), []byte(tc.behavior), 0644); err != nil {
				t.Fatal(err)
			}
			_, stdout, stderr := run(t, "doctor", "--dir", tc.dir, "--offline", "--json")
			var reports []struct{ ID, Status, Message, Try string }
			if err := json.Unmarshal([]byte(stdout), &reports); err != nil {
				t.Fatalf("doctor output: %v stdout=%s stderr=%s", err, stdout, stderr)
			}
			for _, report := range reports {
				if report.ID == "plugin/hello/hello" {
					if report.Status != tc.status || report.Message != "Review hello" || report.Try != "repair hello" {
						t.Fatalf("plugin report=%+v", report)
					}
					return
				}
			}
			t.Fatalf("plugin check missing: %s", stdout)
		})
	}
}

func TestDoctorWithoutExecPlugins(t *testing.T) {
	// Keep the cache isolated without running beside tests that change HOME.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, scenario := range []string{"outside", "no-plugins", "pack-plugin"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			if scenario != "outside" {
				if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte("[app]\nname='demo'\nmodule='example.com/demo'\n"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := manifest.New().Save(filepath.Join(root, "spinup.lock")); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "pack-plugin" {
				source := filepath.Join(root, "plugin")
				if err := os.Mkdir(source, 0755); err != nil {
					t.Fatal(err)
				}
				for path, data := range map[string]string{
					"plugin.toml": "name='declarative'\nversion='1'\napi=1\nspinup='*'\n",
					"pack.toml":   "name='declarative'\n",
				} {
					if err := os.WriteFile(filepath.Join(source, path), []byte(data), 0644); err != nil {
						t.Fatal(err)
					}
				}
				if code, _, stderr := run(t, "plugin", "add", "path:./plugin", "--dir", root, "--trust"); code != ui.ExitOK {
					t.Fatalf("plugin add: code=%d stderr=%s", code, stderr)
				}
			}
			var expected, diagnostic bytes.Buffer
			printer := ui.New(strings.NewReader(""), &expected, &diagnostic, ui.Options{JSON: true})
			err := drift.Doctor(t.Context(), drift.Options{Dir: root, Version: Version, Offline: true, JSON: true}, false, execx.OSRunner{}, printer)
			code, stdout, stderr := run(t, "doctor", "--dir", root, "--offline", "--json")
			if code != ui.ExitCode(err) || stdout != expected.String() {
				t.Fatalf("doctor changed: code=%d stdout=%s stderr=%s, want code=%d stdout=%s", code, stdout, stderr, ui.ExitCode(err), expected.String())
			}
		})
	}
}
