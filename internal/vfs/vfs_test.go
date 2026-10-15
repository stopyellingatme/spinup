package vfs

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestCleanPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "normalizes", path: "dir//file.txt", want: "dir/file.txt"},
		{name: "rejects empty", path: ""},
		{name: "rejects current directory", path: "."},
		{name: "rejects absolute", path: "/file.txt"},
		{name: "rejects parent", path: "dir/../file.txt"},
		{name: "rejects backslash", path: "dir\\file.txt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := CleanPath(test.path)
			if test.want == "" {
				if err == nil {
					t.Fatal("CleanPath() error = nil")
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("CleanPath() = %q, %v; want %q, nil", got, err, test.want)
			}
		})
	}
}

func TestPlan(t *testing.T) {
	t.Parallel()

	var plan Plan
	if err := plan.Add(File{Path: "b.txt", Data: []byte("b")}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if err := plan.Add(File{Path: "a.txt", Data: []byte("a")}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if err := plan.Add(File{Path: "a.txt"}); err == nil {
		t.Fatal("Add() error = nil for duplicate")
	}
	if got := plan.Paths(); strings.Join(got, ",") != "a.txt,b.txt" {
		t.Fatalf("Paths() = %v", got)
	}
	if got := plan.Sorted().Files[0].Path; got != "a.txt" {
		t.Fatalf("Sorted().Files[0].Path = %q", got)
	}
	if got, ok := plan.Get("b.txt"); !ok || string(got.Data) != "b" {
		t.Fatalf("Get() = %#v, %v", got, ok)
	}
	if _, err := plan.Merge(Plan{Files: []File{{Path: "b.txt"}}}); err == nil {
		t.Fatal("Merge() error = nil for conflict")
	}
	merged, err := plan.Merge(Plan{Files: []File{{Path: "c.txt"}}})
	if err != nil || strings.Join(merged.Paths(), ",") != "a.txt,b.txt,c.txt" {
		t.Fatalf("Merge() = %#v, %v", merged, err)
	}
}

func TestDiff(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "unchanged.txt", "same\n")
	writeFile(t, root, "update.txt", "old\nkeep\n")
	writeFile(t, root, "remove.txt", "remove\n")
	writeFile(t, root, "mode.txt", "mode\n")

	plan := mustPlan(t,
		File{Path: "unchanged.txt", Data: []byte("same\n")},
		File{Path: "create.txt", Data: []byte("new\n")},
		File{Path: "update.txt", Data: []byte("new\nkeep\n")},
		File{Path: "remove.txt", Delete: true},
		File{Path: "mode.txt", Data: []byte("mode\n"), Mode: 0o600},
	)
	changes, err := Diff(root, plan)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	want := map[string]ChangeKind{
		"unchanged.txt": Unchanged, "create.txt": Create, "update.txt": Update,
		"remove.txt": Remove, "mode.txt": ModeOnly,
	}
	for _, change := range changes {
		if change.Kind != want[change.Path] {
			t.Errorf("%s kind = %v; want %v", change.Path, change.Kind, want[change.Path])
		}
	}
	var update Change
	for _, change := range changes {
		if change.Path == "update.txt" {
			update = change
		}
	}
	const wantDiff = "--- a/update.txt\n+++ b/update.txt\n@@ -1,2 +1,2 @@\n-old\n+new\n keep\n"
	if update.UnifiedDiff != wantDiff {
		t.Fatalf("UnifiedDiff = %q; want %q", update.UnifiedDiff, wantDiff)
	}
}

func TestDiffBinary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "binary.bin", "old")
	changes, err := Diff(root, mustPlan(t, File{Path: "binary.bin", Data: []byte{'n', 0, 'w'}}))
	if err != nil {
		t.Fatal(err)
	}
	if got := changes[0].UnifiedDiff; got != "Binary files differ" {
		t.Fatalf("UnifiedDiff = %q", got)
	}
}

func TestDiffRejectsExternalSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	_, err := Diff(root, mustPlan(t, File{Path: "outside/file.txt", Data: []byte("no")}))
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("Diff() error = %v", err)
	}
}

func TestApplyDryRunAndSuccess(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "old.txt", "old")
	plan := mustPlan(t,
		File{Path: "old.txt", Data: []byte("new"), Mode: 0o600},
		File{Path: "nested/new.txt", Data: []byte("created")},
	)
	if _, err := Apply(root, plan, ApplyOptions{DryRun: true}); err != nil {
		t.Fatalf("Apply(DryRun) error = %v", err)
	}
	if got := readFile(t, root, "old.txt"); got != "old" {
		t.Fatalf("dry-run changed old.txt to %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "nested", "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created file: %v", err)
	}
	if _, err := Apply(root, plan, ApplyOptions{}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got := readFile(t, root, "old.txt"); got != "new" {
		t.Fatalf("old.txt = %q", got)
	}
	info, err := os.Stat(filepath.Join(root, "old.txt"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("old.txt mode = %v, %v", info.Mode(), err)
	}
	if got := readFile(t, root, "nested/new.txt"); got != "created" {
		t.Fatalf("new.txt = %q", got)
	}
}

func TestApplyRollsBack(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "first.txt", "first")
	if err := os.Chmod(filepath.Join(root, "first.txt"), 0o775); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "second.txt", "second")
	oldHook := applyHook
	applyHook = func(path string) error {
		if path == "second.txt" {
			return fs.ErrPermission
		}
		return nil
	}
	t.Cleanup(func() { applyHook = oldHook })

	_, err := Apply(root, mustPlan(t,
		File{Path: "first.txt", Data: []byte("changed")},
		File{Path: "second.txt", Data: []byte("changed")},
	), ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "second.txt") {
		t.Fatalf("Apply() error = %v", err)
	}
	if got := readFile(t, root, "first.txt"); got != "first" {
		t.Fatalf("first.txt after rollback = %q", got)
	}
	info, statErr := os.Stat(filepath.Join(root, "first.txt"))
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode().Perm() != 0o775 {
		t.Fatalf("first.txt mode after rollback = %o; want 775", info.Mode().Perm())
	}
	if got := readFile(t, root, "second.txt"); got != "second" {
		t.Fatalf("second.txt after rollback = %q", got)
	}
}

func TestPlanFS(t *testing.T) {
	t.Parallel()
	plan := mustPlan(t,
		File{Path: "root.txt", Data: []byte("root"), Mode: 0o600},
		File{Path: "dir/file.txt", Data: []byte("file")},
		File{Path: "gone.txt", Delete: true},
	)
	if err := fstest.TestFS(plan.FS(), "root.txt", "dir/file.txt"); err != nil {
		t.Fatalf("TestFS() error = %v", err)
	}
}

func mustPlan(t *testing.T, files ...File) Plan {
	t.Helper()
	var plan Plan
	for _, file := range files {
		if err := plan.Add(file); err != nil {
			t.Fatal(err)
		}
	}
	return plan
}

func writeFile(t *testing.T, root, path, data string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, root, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
