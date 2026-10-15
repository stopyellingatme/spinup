package conformance

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConformance(t *testing.T) {
	t.Parallel()
	binary := filepath.Join(t.TempDir(), "plugin")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, "./testdata/plugin")
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, data)
	}
	t.Run("good", func(t *testing.T) { t.Parallel(); Run(t, binary) })
	for _, tc := range []struct{ mode, want string }{
		{"api", "handshake"}, {"name", "handshake"}, {"capability", "capability"},
		{"describe", "describe"}, {"missing-capability", "capability"},
		{"nondeterministic", "determinism"}, {"traversal", "path"}, {"protected", "path"},
		{"write", "changed"}, {"parent-write", "changed"}, {"modify", "changed"}, {"chmod", "changed"},
		{"class", "class"}, {"mode", "mode"}, {"duplicate", "duplicate"},
		{"finding", "finding"}, {"malicious", "path"}, {"huge", "path"},
		{"unknown", "unknown"}, {"malformed", "malformed"}, {"slow", "deadline"},
		{"envelope", "JSON-RPC"}, {"shape", "files"}, {"oversize", "exceeds"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()
			sandbox := t.TempDir()
			root := filepath.Join(sandbox, "work")
			if err := os.Mkdir(root, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "behavior"), []byte(tc.mode), 0644); err != nil {
				t.Fatal(err)
			}
			err := run(binary, sandbox, root, Options{Timeout: time.Second})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run(%s) = %v, want %s", tc.mode, err, tc.want)
			}
		})
	}
}

func TestSafePaths(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"../out", "/tmp/out", `C:\out`, "a/../b", "a//b", "a/./b", ".git/config", "nested/.GiT/config", ".SPINUP/a", ".github/workflows/a", "spinup.lock", "SPINUP.TOML", "a\x00", "a\n", "a.", "a ", "CON", "com1.txt", "LPT9"} {
		if err := safePath(name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	for _, name := range []string{"notes/sample.txt", "src/a.go", "nested/.github/a"} {
		if err := safePath(name); err != nil {
			t.Errorf("rejected %q: %v", name, err)
		}
	}
}
