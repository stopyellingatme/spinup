package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginNewFlags(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	code, stdout, stderr := run(t, "plugin", "new", "hello", "--kind", "exec", "--platform", "linux-arm64", "--out", "custom", "--sdk-path", "sdk", "--dir", root, "--json")
	if code != 0 {
		t.Fatalf("plugin new: code=%d stderr=%s", code, stderr)
	}
	var result struct{ Path, Kind string }
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Path != filepath.Join(root, "custom") || result.Kind != "exec" {
		t.Fatalf("result = %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(result.Path, "plugin.toml"))
	if err != nil || !strings.Contains(string(data), `"linux-arm64" = "bin/hello_linux_arm64"`) {
		t.Fatalf("metadata = %s, %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(result.Path, "go.mod"))
	if err != nil || !strings.Contains(string(data), filepath.Join(root, "sdk")) {
		t.Fatalf("go.mod = %s, %v", data, err)
	}
}
