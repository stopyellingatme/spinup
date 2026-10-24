package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestConfirmForReadsGlobalFlags(t *testing.T) {
	t.Setenv("CI", "true")
	app := &App{UI: ui.New(nil, &bytes.Buffer{}, &bytes.Buffer{}, ui.Options{NonInteractive: true}), Flags: &GlobalFlags{Yes: true, ConfirmEnv: "production"}}
	got := confirmFor(app, &cobra.Command{})
	if got.Name != "production" || !got.Yes || !got.CI || got.Interactive {
		t.Fatalf("confirmFor() = %+v", got)
	}
}

func TestConfirmEnvFlagIsGlobal(t *testing.T) {
	t.Parallel()
	code, out, stderr := run(t, "deploy", "--help")
	if code != 0 || !strings.Contains(out+stderr, "--confirm-env") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

// No command builds project.Confirm by hand. Each one calls confirmFor, so
// the flag reaches deploy, backup restore, maintenance, and infra.
func TestCommandsBuildConfirmWithHelper(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"deploy.go", "backup_restore.go", "maintenance_pg.go", "maintenance_pgupgrade.go", "infra.go"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		src := string(data)
		if strings.Contains(src, "project.Confirm{") || !strings.Contains(src, "confirmFor(app, c)") {
			t.Errorf("%s does not build the confirmation with confirmFor", name)
		}
	}
}

// The db commands take the flag through their option fields.
func TestConfirmEnvReachesDBCommands(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	config := "[app]\nname='todo'\n[env.production]\nprotected=true\n"
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"db", "migrate", "down", "--i-know"},
		{"db", "redo", "--i-know"},
	} {
		base := append(append([]string(nil), args...), "--dir", root, "--env", "production", "--dry-run", "--non-interactive")
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			var out, stderr bytes.Buffer
			if code := Execute(context.Background(), base, nil, &out, &stderr); code == 0 || !strings.Contains(stderr.String(), "pass --confirm-env production") {
				t.Fatalf("without the flag: exit=%d, stderr=%s", code, stderr.String())
			}
			out.Reset()
			stderr.Reset()
			if code := Execute(context.Background(), append(base, "--confirm-env", "staging"), nil, &out, &stderr); code == 0 || !strings.Contains(stderr.String(), `--confirm-env has the value "staging"`) {
				t.Fatalf("wrong name: exit=%d, stderr=%s", code, stderr.String())
			}
			out.Reset()
			stderr.Reset()
			if code := Execute(context.Background(), append(base, "--confirm-env", "production"), nil, &out, &stderr); code != 0 || !strings.Contains(out.String(), "go run") {
				t.Fatalf("right name: exit=%d, out=%s, stderr=%s", code, out.String(), stderr.String())
			}
		})
	}
}
