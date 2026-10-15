package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
			c.Backups.Backend = backend
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
			testFlock(t, filepath.Join(dir, "bin"))
			write("run.sh", script, 0755)
			write("backups/backend", []byte(backend+"\n"), 0644)
			write("current/compose.edge.yml", nil, 0644)
			write("current/compose.backups.yml", nil, 0644)
			write("backups/metrics/backup.prom", []byte("# HELP custom Example\n# TYPE custom gauge\ncustom 7\n"), 0644)
			write("bin/docker", []byte(`#!/bin/sh
set -eu
printf '%s %s\n' "$COMPOSE_FILE" "$*" >> "$TRACE"
case "$*" in
 *pg_dump*) [ "${FAIL_BACKUP:-0}" = 0 ] || exit 1; printf 'custom dump' ;;
 *pg_restore*) cat >/dev/null; [ "${FAIL_VERIFY:-0}" = 0 ] ;;
 *' info') printf '[{"backup":[{"type":"diff","info":{"size":42}}]}]' ;;
 *' verify') [ "${FAIL_VERIFY:-0}" = 0 ] ;;
 *' backup') [ "${FAIL_BACKUP:-0}" = 0 ] ;;
esac
`), 0755)
			if backend == "pgdump" {
				for i := 1; i <= 8; i++ {
					write(fmt.Sprintf("backups/pgdump/2000010%dT000000ZF.dump", i), []byte("old dump"), 0600)
				}
			}
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
			if backend == "pgdump" {
				dumps, err := filepath.Glob(filepath.Join(dir, "backups/pgdump/*.dump"))
				if err != nil || len(dumps) != 7 {
					t.Fatalf("dumps kept = %v, %v", dumps, err)
				}
				if _, err := os.Stat(filepath.Join(dir, "backups/pgdump/20000103T000000ZF.dump")); err != nil {
					t.Fatal("pruned a dump within retention", err)
				}
				trace, err := os.ReadFile(filepath.Join(dir, "trace"))
				if err != nil || strings.Contains(string(trace), "compose.backups.yml") {
					t.Fatalf("pgdump enabled archiving: %s, %v", trace, err)
				}
			}
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

// testFlock uses the host flock when available. On macOS, fcntl provides the
// same kernel locks, including the inherited-descriptor form used by run.sh.
func testFlock(t *testing.T, bin string) {
	t.Helper()
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := runexec.LookPath("flock"); err == nil {
		return
	}
	python, err := runexec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	script := `
import fcntl, os, sys
if sys.argv[1].isdigit():
    fcntl.flock(int(sys.argv[1]), fcntl.LOCK_EX)
else:
    f = open(sys.argv[1], 'a')
    fcntl.flock(f.fileno(), fcntl.LOCK_EX)
    os.set_inheritable(f.fileno(), True)
    os.execvp(sys.argv[2], sys.argv[2:])
`
	if err := os.WriteFile(filepath.Join(bin, "flock"), []byte("#!"+python+"\n"+script), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleRemoveScript(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0755); err != nil {
				t.Fatal(err)
			}
			for name, script := range map[string]string{
				"sudo": "#!/bin/sh\nshift\nexec \"$@\"\n",
				"systemctl": `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$TRACE"
case "$1" in
 disable)
   if [ "$FAIL_DISABLE" = true ] && [ "$3" = spinup-todo-backup-full.timer ]; then exit 1; fi
   [ -e "$UNITS/$3" ] || exit 1 ;;
 show)
   if [ "$FAIL_DISABLE" = true ] && [ "$4" = spinup-todo-backup-full.timer ]; then echo loaded; else echo not-found; exit 1; fi ;;
esac
`,
			} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0755); err != nil {
					t.Fatal(err)
				}
			}
			// Only the first kind was installed when the earlier schedule failed.
			for _, ext := range []string{"service", "timer"} {
				if err := os.WriteFile(filepath.Join(dir, "spinup-todo-backup-full."+ext), nil, 0644); err != nil {
					t.Fatal(err)
				}
			}
			cmd := runexec.Cmd{Name: "sh", Args: append([]string{"-ec", scheduleRemoveScript, "spinup-remove", dir}, scheduleTimers("todo")...), Env: []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "UNITS=" + dir, "TRACE=" + filepath.Join(dir, "trace"), "FAIL_DISABLE=" + fmt.Sprint(fail)}}
			for range 2 {
				_, err := (runexec.OSRunner{}).Run(context.Background(), cmd)
				if (err != nil) != fail {
					t.Fatalf("remove error = %v", err)
				}
				for _, timer := range scheduleTimers("todo") {
					for _, name := range []string{timer, strings.TrimSuffix(timer, ".timer") + ".service"} {
						if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
							t.Fatalf("unit remains: %s, %v", name, err)
						}
					}
				}
			}
			trace, err := os.ReadFile(filepath.Join(dir, "trace"))
			if err != nil || strings.Count(string(trace), "daemon-reload") != 2 {
				t.Fatalf("cleanup did not reload: %s, %v", trace, err)
			}
		})
	}
}

func scheduledTestFiles(t *testing.T) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"bin", "current", "backups/metrics"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			t.Fatal(err)
		}
	}
	c := &project.Config{}
	c.App.Name = "todo"
	files, err := scheduleFiles(c, project.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Path == "deploy/backups/run.sh" {
			script := strings.ReplaceAll(string(f.Data), "/opt/$app", dir)
			if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
		}
	}
	bin := filepath.Join(dir, "bin")
	testFlock(t, bin)
	realFlock, err := runexec.LookPath("flock")
	if err != nil {
		realFlock = filepath.Join(bin, "flock-real")
		if err := os.Rename(filepath.Join(bin, "flock"), realFlock); err != nil {
			t.Fatal(err)
		}
	}
	// Report each lock attempt before blocking. FIFOs make the overlap deterministic.
	wrapper := `#!/bin/sh
set -eu
if [ -n "${LOCK_READY:-}" ] && [ "$1" = "$LOCK_MATCH" ]; then printf 'waiting\n' > "$LOCK_READY"; fi
exec "$REAL_FLOCK" "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "flock"), []byte(wrapper), 0755); err != nil {
		t.Fatal(err)
	}
	return dir, []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "REAL_FLOCK=" + realFlock, "BASE=" + dir}
}

func TestMetricsWritersShareLock(t *testing.T) {
	t.Parallel()
	dir, env := scheduledTestFiles(t)
	docker := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "bin/docker"), []byte(docker), 0755); err != nil {
		t.Fatal(err)
	}
	script := `set -eu
cd "$BASE"
mkfifo owner release writers
exec 7<>writers
flock "$BASE/backups/metrics/.lock" sh -ec 'printf "held\n" > owner; read -r release < release' &
owner=$!
trap 'kill "$owner" ${scheduled:-} ${cli:-} 2>/dev/null || true' 0
read -r held < owner
LOCK_READY="$BASE/writers" LOCK_MATCH=8 sh run.sh verify &
scheduled=$!
LOCK_READY="$BASE/writers" LOCK_MATCH="$BASE/backups/metrics/.lock" flock "$BASE/backups/metrics/.lock" sh -ec "$MERGE_SCRIPT" spinup-metrics "$BASE/backups/metrics/backup.prom" <<'METRICS' &
backup_last_success_timestamp_seconds{kind="full"} 123
METRICS
cli=$!
read -r waiting <&7
read -r waiting <&7
# Both writers must read this value after they acquire the lock.
printf '# HELP other Example\nother 99\n' > backups/metrics/backup.prom
printf 'release\n' > release
wait "$owner"
wait "$scheduled"
wait "$cli"
trap - 0
`
	env = append(env, "MERGE_SCRIPT="+metricsMergeScript)
	result, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "sh", Args: []string{"-ec", script}, Env: env, Timeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("overlapping writers: %v\n%s", err, result.Stderr)
	}
	data, err := os.ReadFile(filepath.Join(dir, "backups/metrics/backup.prom"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# HELP other Example\n", "other 99\n", "backup_last_success_timestamp_seconds{kind=\"full\"} 123\n", "backup_last_verify_ok 1\n"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("lost series %q:\n%s", want, data)
		}
	}
}

func TestScheduledRunsShareLock(t *testing.T) {
	t.Parallel()
	dir, env := scheduledTestFiles(t)
	docker := `#!/bin/sh
set -eu
case "$*" in
 *' backup')
    # Only one backup can enter this critical section.
    mkdir "$BASE/active-backup"
    case "$*" in *--type=full*) printf 'started\n' > "$BASE/started"; read -r release < "$BASE/release" ;; esac
    rmdir "$BASE/active-backup" ;;
 *' info') printf '[{"backup":[{"type":"diff","info":{"size":42}}]}]' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "bin/docker"), []byte(docker), 0755); err != nil {
		t.Fatal(err)
	}
	script := `set -eu
cd "$BASE"
mkfifo started release waiting
exec 7<>waiting
sh run.sh full &
first=$!
trap 'kill "$first" ${second:-} 2>/dev/null || true' 0
read -r started < started
LOCK_READY="$BASE/waiting" LOCK_MATCH=9 sh run.sh diff &
second=$!
read -r waiting <&7
printf 'release\n' > release
wait "$first"
wait "$second"
trap - 0
`
	result, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "sh", Args: []string{"-ec", script}, Env: env, Timeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("overlapping jobs: %v\n%s", err, result.Stderr)
	}
}
