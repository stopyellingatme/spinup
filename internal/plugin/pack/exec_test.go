package pack

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/plugin"
)

func execFixture(t *testing.T, extra string) string {
	t.Helper()
	root := fixture(t)
	name := "bin/hello"
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "plugin.toml"))
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte(extra)...)
	if err := os.WriteFile(filepath.Join(root, "plugin.toml"), data, 0644); err != nil {
		t.Fatal(err)
	}
	return root
}
func execConfig() string {
	return "kind='exec'\n[exec]\nbinaries={'" + runtime.GOOS + "-" + runtime.GOARCH + "'='bin/hello'}\n"
}
func TestExecMetadata(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, extra string
		valid       bool
	}{
		{"exec", execConfig(), true}, {"pack", "kind='pack'\n", true},
		{"unknown", "kind='shell'\n", false}, {"missing", "kind='exec'\n", false},
		{"pack exec", "kind='pack'\n[exec]\nbinaries={'linux-amd64'='bin/hello'}", false},
		{"escape", "kind='exec'\n[exec]\nbinaries={'linux-amd64'='../hello'}", false},
		{"missing file", "kind='exec'\n[exec]\nbinaries={'linux-amd64'='bin/missing'}", false},
		{"platform", "kind='exec'\n[exec]\nbinaries={'banana'='bin/hello'}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := execFixture(t, tc.extra)
			tr, err := readTree(root)
			if err != nil {
				t.Fatal(err)
			}
			_, err = validate(tr, "1.2.3")
			if (err == nil) != tc.valid {
				t.Fatalf("validate: %v", err)
			}
		})
	}
}
func TestExecTrust(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		trust              bool
		typed, answer      string
		interactive, valid bool
	}{
		{name: "trust alone", trust: true}, {name: "wrong name", typed: "other"},
		{name: "flag", typed: "example", valid: true},
		{name: "terminal yes", interactive: true, answer: "yes\n"},
		{name: "terminal name", interactive: true, answer: "example\n", valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := manager(t).Root
			source := execFixture(t, execConfig())
			m := Manager{Root: root, CacheDir: t.TempDir(), Trust: tc.trust, TrustExec: tc.typed, Interactive: tc.interactive, In: strings.NewReader(tc.answer)}
			err := m.Add(context.Background(), "path:"+source, "")
			if (err == nil) != tc.valid {
				t.Fatalf("Add: %v", err)
			}
			if !tc.valid {
				if !strings.Contains(err.Error(), "Try:") {
					t.Fatal(err)
				}
				return
			}
			data, err := os.ReadFile(filepath.Join(root, plugin.AllowlistFile))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(string(data), " exec\n") {
				t.Fatalf("allowlist: %s", data)
			}
			// Removing the exec marker must revoke execution approval.
			if err := os.WriteFile(filepath.Join(root, plugin.AllowlistFile), []byte(strings.TrimSuffix(string(data), " exec\n")+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := m.resolve(context.Background(), plugin.Options{Root: root}); err == nil {
				t.Fatal("accepted absent exec marker")
			}
		})
	}
}
func TestExecMetadataFromFS(t *testing.T) {
	t.Parallel()
	root := execFixture(t, execConfig())
	tr, err := readTree(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ReadExec(tr.fs())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Kind != "exec" || len(cfg.Binaries) != 1 {
		t.Fatalf("%+v", cfg)
	}
	if _, err = fs.ReadFile(tr.fs(), "bin/hello"); err != nil {
		t.Fatal(err)
	}
}

func TestExecAllowlistMarkers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		marker string
		valid  bool
	}{{"", true}, {" exec", true}, {" shell", false}, {" exec extra", false}} {
		t.Run(tc.marker, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".spinup"), 0755); err != nil {
				t.Fatal(err)
			}
			line := "hello path:./hello " + strings.Repeat("a", 64) + tc.marker + "\n"
			if err := os.WriteFile(filepath.Join(root, plugin.AllowlistFile), []byte(line), 0644); err != nil {
				t.Fatal(err)
			}
			approvals, err := readAllowlist(root, false)
			if (err == nil) != tc.valid {
				t.Fatalf("%v", err)
			}
			if tc.valid && string(allowBytes(approvals)) != line {
				t.Fatalf("changed allowlist bytes: %s", allowBytes(approvals))
			}
		})
	}
}
