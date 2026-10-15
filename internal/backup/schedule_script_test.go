package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
)

func TestScheduledScript(t *testing.T) {
	t.Parallel()
	for _, backend := range []string{"pgbackrest", "pgdump"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, sub := range []string{"bin", "current", "backups/metrics", "backups/pgdump"} {
				if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
					t.Fatal(err)
				}
			}
			c := &project.Config{}
			c.App.Name = "todo"
			files, err := scheduleFiles(c, project.Env{User: "deploy"})
			if err != nil {
				t.Fatal(err)
			}
			var script []byte
			for _, f := range files {
				if f.Path == "deploy/backups/run.sh" {
					script = f.Data
				}
			}
			if len(script) == 0 {
				t.Fatal("missing script")
			}
			// Relocate only the test copy. Production paths remain fixed under /opt.
			script = []byte(strings.ReplaceAll(string(script), "/opt/$app", dir))
			write := func(name string, data []byte, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), data, mode); err != nil {
					t.Fatal(err)
				}
			}
			write("run.sh", script, 0755)
			write("backups/backend", []byte(backend+"\n"), 0644)
			write("current/compose.edge.yml", nil, 0644)
			write("backups/metrics/backup.prom", []byte("# HELP custom Example\n# TYPE custom gauge\ncustom 7\n"), 0644)
			write("bin/docker", []byte(`#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$TRACE"
case "$*" in
 *pg_dump*) [ "${FAIL_BACKUP:-0}" = 0 ] || exit 1; printf 'custom dump' ;;
 *pg_restore*) cat >/dev/null; [ "${FAIL_VERIFY:-0}" = 0 ] ;;
 *' info') printf '[{"backup":[{"type":"diff","info":{"size":42}}]}]' ;;
 *' verify') [ "${FAIL_VERIFY:-0}" = 0 ] ;;
 *' backup') [ "${FAIL_BACKUP:-0}" = 0 ] ;;
esac
`), 0755)
			runner := runexec.OSRunner{}
			baseEnv := []string{"PATH=" + filepath.Join(dir, "bin") + ":" + os.Getenv("PATH"), "TRACE=" + filepath.Join(dir, "trace")}
			if _, err := runner.Run(context.Background(), runexec.Cmd{Name: "sh", Args: []string{"-n", filepath.Join(dir, "run.sh")}}); err != nil {
				t.Fatal(err)
			}
			run := func(kind string, fail bool) {
				t.Helper()
				env := append([]string(nil), baseEnv...)
				if fail {
					env = append(env, "FAIL_VERIFY=1")
				}
				_, err := runner.Run(context.Background(), runexec.Cmd{Name: "sh", Args: []string{filepath.Join(dir, "run.sh"), kind}, Env: env})
				if (err != nil) != fail {
					t.Fatalf("run %s: %v", kind, err)
				}
			}
			run("full", false)
			run("diff", false)
			run("verify", false)
			run("verify", true)
			data, err := os.ReadFile(filepath.Join(dir, "backups/metrics/backup.prom"))
			if err != nil {
				t.Fatal(err)
			}
			wantKind := "diff"
			size := "42"
			if backend == "pgdump" {
				wantKind = "full"
				size = "11"
			}
			for _, want := range []string{"# HELP custom Example\n", "# TYPE custom gauge\n", "custom 7\n", "backup_last_success_timestamp_seconds{kind=\"" + wantKind + "\"}", "backup_last_size_bytes{kind=\"" + wantKind + "\"} " + size + "\n", "backup_last_verify_ok 0\n", "backup_last_verify_timestamp_seconds ", "backup_last_verify_rto_seconds "} {
				if !strings.Contains(string(data), want) {
					t.Errorf("metrics missing %q:\n%s", want, data)
				}
			}
			info, err := os.Stat(filepath.Join(dir, "backups/metrics/backup.prom"))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0644 {
				t.Fatalf("metrics mode = %v", info.Mode())
			}
			tmp, err := filepath.Glob(filepath.Join(dir, "backups/metrics/backup.prom.tmp.*"))
			if err != nil || len(tmp) > 0 {
				t.Fatalf("temporary files = %v, %v", tmp, err)
			}
			_, err = runner.Run(context.Background(), runexec.Cmd{Name: "sh", Args: []string{filepath.Join(dir, "run.sh"), "full"}, Env: append(baseEnv, "FAIL_BACKUP=1")})
			if err == nil {
				t.Fatal("failed backup succeeded")
			}
			after, err := os.ReadFile(filepath.Join(dir, "backups/metrics/backup.prom"))
			if err != nil || string(after) != string(data) {
				t.Fatalf("failed backup changed metrics: %s, %v", after, err)
			}
		})
	}
}
