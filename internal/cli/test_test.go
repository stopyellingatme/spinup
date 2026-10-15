package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTestCommand(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run(t, "test", "--dir", root, "--run", "Todo", "--race", "--cover", "--dry-run")
	if code != 0 || out != "go test -run Todo -race -cover ./...\n" {
		t.Fatalf("code %d: %s %s", code, out, errOut)
	}
	for _, target := range []string{"web", "e2e", "contract"} {
		code, _, errOut = run(t, "test", "--"+target)
		if code == 0 || !strings.Contains(errOut, "Try:") || !strings.Contains(errOut, "M4") {
			t.Fatalf("%s: code %d: %s", target, code, errOut)
		}
	}
}
