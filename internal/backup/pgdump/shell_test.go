package pgdump

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

func executable(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nset -eu\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestBackupShellAtomicPublish(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "dumps with 'quotes'")
			executable(t, bin, "docker", `[ "$1" = compose ]
shift
[ "$1" = -p ] && [ "$2" = my-app ]
while [ "$1" != exec ]; do shift; done
shift
[ "$1" = -T ]; shift
[ "$1" = --user ] && [ "$2" = postgres ]; shift 2
[ "$1" = postgres ]; shift
exec "$@"
`)
			executable(t, bin, "pg_dump", `[ "$1" = -Fc ] && [ "$2" = -U ] && [ "$3" = "$POSTGRES_USER" ]
[ "$4" = --dbname ] && [ "$5" = "$POSTGRES_DB" ]
printf PGDMP
if [ "$FAIL_DUMP" = 1 ]; then exit 42; fi
`)
			// The VM has GNU stat. This portable fixture supplies only its size operation.
			executable(t, bin, "stat", `[ "$1" = -c ] && [ "$2" = %s ] && [ "$3" = -- ]
wc -c < "$4"
`)
			flag := "0"
			if fail {
				flag = "1"
			}
			cmd := runexec.Cmd{Name: "sh", Args: []string{"-eu", "-c", backupScript, "pgdump", "my-app", dir, label}, Env: []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "POSTGRES_USER=user ' quoted", "POSTGRES_DB=db; $(false)", "FAIL_DUMP=" + flag}}
			_, err := (runexec.OSRunner{}).Run(context.Background(), cmd)
			if (err != nil) != fail {
				t.Fatalf("backup shell = %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, label+".dump.tmp")); !os.IsNotExist(err) {
				t.Fatalf("temporary dump remains: %v", err)
			}
			dest := filepath.Join(dir, label+".dump")
			if fail {
				if _, err := os.Stat(dest); !os.IsNotExist(err) {
					t.Fatalf("failed dump published: %v", err)
				}
				return
			}
			info, err := os.Stat(dest)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 || info.Size() != 5 {
				t.Fatalf("dump mode=%o size=%d", info.Mode().Perm(), info.Size())
			}
			// The same label must fail without replacing the complete dump.
			if _, err := (runexec.OSRunner{}).Run(context.Background(), cmd); err == nil {
				t.Fatal("duplicate label overwrote a dump")
			}
			contents, err := os.ReadFile(dest)
			if err != nil || string(contents) != "PGDMP" {
				t.Fatalf("dump = %q, %v", contents, err)
			}
		})
	}
}

func TestCleanupShellOwnership(t *testing.T) {
	t.Parallel()
	for _, own := range []bool{false, true} {
		t.Run(map[bool]string{false: "other container", true: "owned container"}[own], func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			log := filepath.Join(root, "removed")
			executable(t, root, "docker", `case "$1" in
ps)
 [ "$2" = -aq ] && [ "$3" = --filter ] && [ "$4" = 'name=^/my-app-verify-20261015T120000ZF$' ]
 [ "$5" = --filter ] && [ "$6" = 'label=spinup.pgdump.operation=owner' ]
 if [ "$OWN" = 1 ]; then printf 'container-id\n'; fi
 ;;
rm)
 [ "$2" = -f ] && [ "$3" = -v ] && [ "$4" = -- ] && [ "$5" = container-id ]
 printf removed > "$REMOVE_LOG"
 ;;
*) exit 1 ;;
esac
`)
			flag := "0"
			if own {
				flag = "1"
			}
			cmd := runexec.Cmd{Name: "sh", Args: []string{"-eu", "-c", cleanupScript, "pgdump", "my-app-verify-" + label, "owner"}, Env: []string{"PATH=" + root + ":" + os.Getenv("PATH"), "OWN=" + flag, "REMOVE_LOG=" + log}}
			if _, err := (runexec.OSRunner{}).Run(context.Background(), cmd); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(log)
			if own {
				if err != nil || strings.TrimSpace(string(data)) != "removed" {
					t.Fatalf("cleanup=%q, %v", data, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("removed another container: %v", err)
			}
		})
	}
}
