package pgupgrade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

func TestRemoveUnlinkedTargetShell(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"owned", "absent", "unowned", "linked target", "linked parent", "linked marker", "disabled source"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			write(t, root, "old/PG_VERSION", "17")
			write(t, root, "old/global/pg_control", "original")
			if name != "absent" {
				write(t, root, "18/docker/keep", "target")
			}
			if name != "unowned" && name != "absent" {
				write(t, root, "18/docker.spinup-upgrade", "")
			}
			switch name {
			case "linked target":
				if err := os.RemoveAll(filepath.Join(root, "18/docker")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "old"), filepath.Join(root, "18/docker")); err != nil {
					t.Fatal(err)
				}
			case "linked parent":
				if err := os.Rename(filepath.Join(root, "18"), filepath.Join(root, "saved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "saved"), filepath.Join(root, "18")); err != nil {
					t.Fatal(err)
				}
			case "linked marker":
				if err := os.Remove(filepath.Join(root, "18/docker.spinup-upgrade")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "old/PG_VERSION"), filepath.Join(root, "18/docker.spinup-upgrade")); err != nil {
					t.Fatal(err)
				}
			case "disabled source":
				if err := os.Rename(filepath.Join(root, "old/global/pg_control"), filepath.Join(root, "old/global/pg_control.spinup-disabled")); err != nil {
					t.Fatal(err)
				}
			}
			script := strings.NewReplacer("/pg/", root+"/", "current=/pg", "current="+root).Replace(removeUnlinkedScript)
			result, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "/bin/sh", Args: []string{"-ec", script, "1000:1000", "old", "18/docker"}})
			wantSuccess := name == "owned" || name == "absent"
			if (err == nil) != wantSuccess {
				t.Fatalf("cleanup = %v, stderr %s", err, result.Stderr)
			}
			if got, err := os.ReadFile(filepath.Join(root, "old/PG_VERSION")); err != nil || string(got) != "17" {
				t.Fatalf("source changed: %q, %v", got, err)
			}
			if wantSuccess {
				for _, path := range []string{"18/docker", "18/docker.spinup-upgrade"} {
					if _, err := os.Lstat(filepath.Join(root, path)); !os.IsNotExist(err) {
						t.Fatalf("target remains: %s, %v", path, err)
					}
				}
			} else if name != "linked target" {
				if got, err := os.ReadFile(filepath.Join(root, "18/docker/keep")); err != nil || string(got) != "target" {
					t.Fatalf("unowned target changed: %q, %v", got, err)
				}
			}
		})
	}
}

func TestArchiveRecoveryShell(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"success", "existing archive", "linked archive", "linked source"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			source := filepath.Join(root, "pg-upgrade")
			archive := source + ".17-18"
			write(t, source, "rollback.sh", "retained")
			switch name {
			case "existing archive":
				write(t, archive, "keep", "earlier")
			case "linked archive":
				if err := os.Symlink(filepath.Join(root, "missing"), archive); err != nil {
					t.Fatal(err)
				}
			case "linked source":
				if err := os.Rename(source, filepath.Join(root, "saved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "saved"), source); err != nil {
					t.Fatal(err)
				}
			}
			result, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "/bin/sh", Args: []string{"-ec", archiveGuard + "\nmv -- \"$1\" \"$2\"", "pg-upgrade", source, archive}})
			if name == "success" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(source); !os.IsNotExist(err) {
					t.Fatalf("operation directory still reserved: %v", err)
				}
				if got, err := os.ReadFile(filepath.Join(archive, "rollback.sh")); err != nil || string(got) != "retained" {
					t.Fatalf("recovery files lost: %q %v", got, err)
				}
			} else {
				if err == nil || !strings.Contains(string(result.Stderr), "Try:") {
					t.Fatalf("unsafe archive succeeded: %v, %s", err, result.Stderr)
				}
				if got, err := os.ReadFile(filepath.Join(source, "rollback.sh")); err != nil || string(got) != "retained" {
					t.Fatalf("source changed: %q %v", got, err)
				}
			}
		})
	}
}

func TestFailedInitializationTargetCanBeRemoved(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "old/PG_VERSION", "17")
	write(t, root, "old/global/pg_control", "original")
	bin := t.TempDir()
	for name, script := range map[string]string{
		"chown":  "#!/bin/sh\nexit 0\n",
		"gosu":   "#!/bin/sh\nshift\nexec \"$@\"\n",
		"initdb": "#!/bin/sh\nprintf partial > \"$1/partial\"\nexit 42\n",
	} {
		if err := runexec.WriteExecutable(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	replace := strings.NewReplacer("/pg/", root+"/", "current=/pg", "current="+root)
	cmd := runexec.Cmd{Name: "/bin/sh", Args: []string{"-ec", replace.Replace(initScript), "1000:1000", "old", "18/docker", "initdb", filepath.Join(root, "18/docker")}, Env: []string{"PATH=" + bin + ":/usr/bin:/bin"}}
	result, err := (runexec.OSRunner{}).Run(context.Background(), cmd)
	if err == nil || result.ExitCode != 42 {
		t.Fatalf("expected initdb failure: %v, %s", err, result.Stderr)
	}
	cmd.Args = []string{"-ec", replace.Replace(removeUnlinkedScript), "1000:1000", "old", "18/docker"}
	if _, err := (runexec.OSRunner{}).Run(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "18/docker")); !os.IsNotExist(err) {
		t.Fatalf("partially initialized directory remains: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "old/global/pg_control")); err != nil || string(got) != "original" {
		t.Fatalf("source changed: %q %v", got, err)
	}
}
