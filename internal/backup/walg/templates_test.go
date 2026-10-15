package walg

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor/composelint"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func renderBackups(t *testing.T) fs.FS {
	t.Helper()
	return testutil.RenderPack(t, []string{"base", "api", "deploy", "backups"}, map[string]any{"app_name": "todo", "module": "example.com/todo", "modules": "deploy,backups"})
}

func readTemplate(t *testing.T, tree fs.FS, name string) string {
	t.Helper()
	data, err := fs.ReadFile(tree, name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestWALGTemplates(t *testing.T) {
	t.Parallel()
	tree := renderBackups(t)
	compose := readTemplate(t, tree, "deploy/compose.backups-walg.yml")
	loader := strings.ReplaceAll(SecretLoader, "$", "$$")
	if !strings.Contains(compose, loader) {
		t.Fatal("Compose secret loader differs from the backend")
	}
	findings, err := composelint.Lint("deploy/compose.backups-walg.yml", []byte(compose))
	if err != nil || len(findings) != 0 {
		t.Fatalf("Compose lint: %+v, %v", findings, err)
	}
	dockerfile := readTemplate(t, tree, "deploy/Dockerfile.postgres")
	walg := stackversions.MustLoad().Binaries["wal-g"]
	for _, want := range []string{walg.Version, walg.Assets["linux-amd64"].Name, walg.Assets["linux-arm64"].Name, walg.Assets["linux-amd64"].SHA256, walg.Assets["linux-arm64"].SHA256, "sha256sum -c -", "ARG TARGETARCH", "libsodium23"} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("Dockerfile missing %q", want)
		}
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(script, []byte(readTemplate(t, tree, "deploy/backups/run.sh")), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "sh", Args: []string{"-n", script}}); err != nil {
		t.Fatal(err)
	}
}

func TestScheduledWALG(t *testing.T) {
	t.Parallel()
	for _, fail := range []string{"", "create", "start", "check", "backup", "cleanup"} {
		t.Run(fail, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, name := range []string{"bin", "current", "backups/metrics"} {
				if err := os.MkdirAll(filepath.Join(dir, name), 0755); err != nil {
					t.Fatal(err)
				}
			}
			write := func(name, data string, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), mode); err != nil {
					t.Fatal(err)
				}
			}
			script := strings.ReplaceAll(readTemplate(t, renderBackups(t), "deploy/backups/run.sh"), "/opt/$app", dir)
			write("run.sh", script, 0700)
			write("backups/backend", "walg\n", 0600)
			write("backups/metrics/backup.prom", "custom 7\n", 0644)
			// These tests run one script at a time in an isolated directory.
			// Production uses flock. The existing schedule tests cover its locking behavior.
			write("bin/flock", "#!/bin/sh\nexit 0\n", 0700)
			write("bin/docker", `#!/bin/sh
set -eu
printf '%s %s\n' "${COMPOSE_FILE-}" "$*" >> "$TRACE"
case "$1" in
 compose)
 case "$*" in
 *'ps -aq postgres') echo live ;;
 *'backup-list --json --detail') cat "$FIXTURE" ;;
 *'backup-push'*) [ "$FAIL" != backup ] ;;
 esac ;;
 inspect) printf '%s' "$INSPECTION" ;;
 create) [ "$FAIL" != create ] ;;
 start) [ "$FAIL" != start ] ;;
 exec) [ "$FAIL" != check ]; echo 2 ;;
 ps) echo owned ;;
 rm) [ "$FAIL" != cleanup ] ;;
esac
`, 0700)
			fixturePath := filepath.Join(dir, "listing.json")
			write("listing.json", fixture(t), 0600)
			env := []string{"PATH=" + filepath.Join(dir, "bin") + ":" + os.Getenv("PATH"), "TRACE=" + filepath.Join(dir, "trace"), "FIXTURE=" + fixturePath, "INSPECTION=" + inspection, "FAIL=" + fail}
			kind := "verify"
			if fail == "backup" {
				kind = "full"
			}
			_, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "sh", Args: []string{filepath.Join(dir, "run.sh"), kind}, Env: env})
			if (err != nil) != (fail != "") {
				t.Fatalf("scheduled %s: %v", kind, err)
			}
			metrics, readErr := os.ReadFile(filepath.Join(dir, "backups/metrics/backup.prom"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !strings.Contains(string(metrics), "custom 7\n") {
				t.Fatal(string(metrics))
			}
			if fail == "backup" {
				if string(metrics) != "custom 7\n" {
					t.Fatal(string(metrics))
				}
				return
			}
			ok := "1"
			if fail != "" {
				ok = "0"
			}
			if !strings.Contains(string(metrics), "backup_last_verify_ok "+ok+"\n") {
				t.Fatal(string(metrics))
			}
			trace, err := os.ReadFile(filepath.Join(dir, "trace"))
			if err != nil {
				t.Fatal(err)
			}
			s := string(trace)
			for _, want := range []string{"compose.edge.yml:compose.backups-walg.yml", "wal-g backup-fetch", "wal-g wal-fetch", "rm -fv -- owned"} {
				if !strings.Contains(s, want) {
					t.Errorf("missing %q: %s", want, s)
				}
			}
			if fail != "create" && fail != "start" && !strings.Contains(s, "goose_db_version") {
				t.Fatal("verification did not check goose_db_version")
			}
			if strings.Contains(s, "compose.backups.yml") {
				t.Fatal("loaded pgBackRest override")
			}
			if fail == "" {
				_, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "sh", Args: []string{filepath.Join(dir, "run.sh"), "diff"}, Env: env})
				if err != nil {
					t.Fatal(err)
				}
				metrics, err = os.ReadFile(filepath.Join(dir, "backups/metrics/backup.prom"))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(metrics), `backup_last_size_bytes{kind="incr"} 2048`) {
					t.Fatal(string(metrics))
				}
			}
		})
	}
}
