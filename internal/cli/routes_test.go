package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoutesCommand(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run(t, "routes", "--dir", root, "--json")
	if code != 0 || strings.Count(out, `"source":"kit"`) != 3 {
		t.Fatalf("code %d: %s %s", code, out, errOut)
	}
}
