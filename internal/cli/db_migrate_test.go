package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDBMigrateProtectedDefaultDryRun(t *testing.T) {
	t.Parallel()
	for _, config := range []string{
		"default_env='prod'\n[env.prod]\nprotected=true\n",
		"[env.prod]\nprotected=true\n",
	} {
		t.Run(config, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte(config+"[app]\nname='todo'\n"), 0644); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			code := Execute(context.Background(), []string{"db", "migrate", "down", "--dir", root, "--i-know", "--non-interactive", "--dry-run"}, nil, &out, &stderr)
			if code == 0 || !strings.Contains(stderr.String(), "protected environment") || !strings.Contains(stderr.String(), "Try:") {
				t.Fatalf("exit=%d, stderr=%s", code, stderr.String())
			}
			if strings.Contains(out.String(), "go run") {
				t.Fatalf("refused migration printed a command: %s", out.String())
			}
		})
	}
}
