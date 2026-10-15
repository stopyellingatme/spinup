package pgupgrade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

func TestHelperPreservesArgumentsAndUser(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "getent", "#!/bin/sh\nexit 0\n")
	write(t, dir, "gosu", "#!/bin/sh\nprintf '%s\\n' \"$1\"\nshift\nexec \"$@\"\n")
	for _, name := range []string{"getent", "gosu"} {
		if err := os.Chmod(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range []string{"1000:1000", "1234:4321"} {
		t.Run(user, func(t *testing.T) {
			t.Parallel()
			r, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "/bin/sh", Args: []string{"-ec", userScript, "pg-upgrade", user, execScript, "printf", "%s\\n", "argument with spaces", "quote'and$dollar"}, Env: []string{"PATH=" + dir + ":/usr/bin:/bin"}})
			if err != nil || string(r.Stdout) != user+"\nargument with spaces\nquote'and$dollar\n" {
				t.Fatalf("got %q, %v", r.Stdout, err)
			}
		})
	}
}

func TestInitializationRefusesExistingOrLinkedDirectory(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"existing target", "linked source parent", "linked target parent"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			write(t, root, "old/data/PG_VERSION", "17\n")
			if name == "existing target" {
				write(t, root, "18/docker/marker", "keep")
			}
			if name == "linked source parent" {
				if err := os.Rename(filepath.Join(root, "old"), filepath.Join(root, "saved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "saved"), filepath.Join(root, "old")); err != nil {
					t.Fatal(err)
				}
			}
			if name == "linked target parent" {
				if err := os.Symlink(filepath.Join(root, "old"), filepath.Join(root, "18")); err != nil {
					t.Fatal(err)
				}
			}
			script := strings.ReplaceAll(initScript, "/pg", root)
			r, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "/bin/sh", Args: []string{"-ec", script, "1000:1000", "old/data", "18/docker", "/bin/false"}})
			if err == nil {
				t.Fatalf("unsafe initialization succeeded: %s", r.Stdout)
			}
			if b, e := os.ReadFile(filepath.Join(root, "old/data/PG_VERSION")); e != nil || string(b) != "17\n" {
				t.Fatalf("source changed: %q %v", b, e)
			}
			if name == "existing target" {
				if b, e := os.ReadFile(filepath.Join(root, "18/docker/marker")); e != nil || string(b) != "keep" {
					t.Fatalf("target changed: %q %v", b, e)
				}
			}
		})
	}
}

func TestRuntimeUserRefusals(t *testing.T) {
	t.Parallel()
	for _, user := range []string{"", "root", "0:0", "1000", "1000:staff", "-1:1", "1000:1000:1"} {
		if validUser(user) {
			t.Errorf("accepted %q", user)
		}
	}
}
