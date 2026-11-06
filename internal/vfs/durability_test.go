package vfs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReservedDeviceCommonPathsDoNotAllocate(t *testing.T) {
	for _, name := range []string{"main", "api", "web", "internal", "config", "note", "jobs"} {
		if allocations := testing.AllocsPerRun(100, func() {
			if reservedDevice(name) {
				t.Fatalf("reservedDevice(%q) = true", name)
			}
		}); allocations != 0 {
			t.Errorf("reservedDevice(%q): %g allocations; want zero", name, allocations)
		}
	}
}

func TestSyncFile(t *testing.T) {
	t.Parallel()
	file, err := os.CreateTemp(t.TempDir(), "sync-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if _, err := file.WriteString("durable"); err != nil {
		t.Fatal(err)
	}
	if err := SyncFile(file); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := SyncFile(file); err == nil {
		t.Fatal("SyncFile accepted a closed file")
	}
}

func TestApplyChecksCreatedParents(t *testing.T) {
	t.Parallel()
	for _, names := range [][2]string{{"Dir/a", "dir/b"}, {"K/a", "K/b"}} {
		root := t.TempDir()
		_, err := Apply(root, mustPlan(t,
			File{Path: names[0], Data: []byte("first")},
			File{Path: names[1], Data: []byte("second")},
		), ApplyOptions{})
		assertPathError(t, err, "case folding")
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("rollback left entries: %v", entries)
		}
	}
}

func TestApplyChecksDirectoriesAgain(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	plan := mustPlan(t, File{Path: "dir/a", Data: []byte("first")})
	if _, err := Apply(root, plan, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "dir/SECOND", "existing")
	_, err := Apply(root, mustPlan(t, File{Path: "dir/second", Data: []byte("replacement")}), ApplyOptions{})
	assertPathError(t, err, "case folding", "SECOND")
	if got := readFile(t, root, "dir/SECOND"); got != "existing" {
		t.Fatalf("case collision changed the file: %q", got)
	}
}

func TestApplyWritesRenamesAndRollsBack(t *testing.T) {
	// The hook is global, so these subtests must remain serial.
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "rename failure"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "b-update", "old")
			writeFile(t, root, "c-delete", "removed")
			writeFile(t, root, "d-mode", "mode")
			original, err := os.Stat(filepath.Join(root, "b-update"))
			if err != nil {
				t.Fatal(err)
			}
			deleted, err := os.Stat(filepath.Join(root, "c-delete"))
			if err != nil {
				t.Fatal(err)
			}
			// On Windows, os.Stat loads the file ID on the first os.SameFile call, by path.
			// Load the IDs now, so that they identify the files from before Apply.
			_ = os.SameFile(original, original)
			_ = os.SameFile(deleted, deleted)
			oldHook := applyHook
			t.Cleanup(func() { applyHook = oldHook })
			applyHook = func(name string) error {
				if fail && name == "z-fail" {
					// Force the final rename to fail at the real filesystem boundary.
					return os.Mkdir(filepath.Join(root, name), 0o755)
				}
				return nil
			}
			_, err = Apply(root, mustPlan(t,
				File{Path: "a-new/nested/file", Data: []byte("created")},
				File{Path: "b-update", Data: []byte("new"), Mode: 0o600},
				File{Path: "c-delete", Delete: true},
				File{Path: "d-mode", Data: []byte("mode"), Mode: 0o600},
				File{Path: "z-fail", Data: []byte("last")},
			), ApplyOptions{})
			if fail {
				if err == nil || !strings.Contains(err.Error(), "z-fail") {
					t.Fatalf("Apply() = %v; want final rename failure", err)
				}
				if got := readFile(t, root, "b-update"); got != "old" {
					t.Fatalf("restored content = %q", got)
				}
				if got := readFile(t, root, "c-delete"); got != "removed" {
					t.Fatalf("restored deletion = %q", got)
				}
				restored, err := os.Stat(filepath.Join(root, "c-delete"))
				if err != nil || !os.SameFile(deleted, restored) {
					t.Fatalf("rollback did not rename the deleted file back: %v", err)
				}
				if _, err := os.Stat(filepath.Join(root, "a-new")); !os.IsNotExist(err) {
					t.Fatalf("rollback left created directories: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if got := readFile(t, root, "b-update"); got != "new" {
					t.Fatalf("updated content = %q", got)
				}
				updated, err := os.Stat(filepath.Join(root, "b-update"))
				if err != nil || os.SameFile(original, updated) {
					t.Fatalf("Apply did not replace the file by rename: %v", err)
				}
				if got := readFile(t, root, "a-new/nested/file"); got != "created" {
					t.Fatalf("created content = %q", got)
				}
				if _, err := os.Stat(filepath.Join(root, "c-delete")); !os.IsNotExist(err) {
					t.Fatalf("Apply left the deleted file: %v", err)
				}
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(filepath.Join(root, "d-mode"))
				if err != nil {
					t.Fatal(err)
				}
				want := os.FileMode(0o600)
				if fail {
					want = 0o644
				}
				if info.Mode().Perm() != want {
					t.Fatalf("mode = %o; want %o", info.Mode().Perm(), want)
				}
			}
			if err := filepath.WalkDir(root, func(name string, _ os.DirEntry, err error) error {
				if err == nil && strings.HasPrefix(filepath.Base(name), ".spinup-vfs-") {
					t.Errorf("Apply left temporary storage: %s", name)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
