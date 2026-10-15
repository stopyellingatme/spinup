package manifest

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, path, data string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestStatus(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	l := New()
	l.Files["z/modified"] = tracked(Managed)
	l.Files["a/unchanged"] = tracked(Scaffold)
	l.Files["m/missing"] = tracked(Managed)
	l.Files["seed"] = FileEntry{Class: Seed}
	// A tombstone takes precedence even if an in-memory entry still exists.
	l.Files["removed"] = tracked(Scaffold)
	l.Tombstones = []string{"removed"}
	writeFile(t, root, "z/modified", "user change\n")
	writeFile(t, root, "a/unchanged", "original\r\n\r\n")
	got, err := l.Status(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []FileStatus{
		{Path: "a/unchanged", Entry: tracked(Scaffold), State: Unchanged},
		{Path: "m/missing", Entry: tracked(Managed), State: Missing},
		{Path: "z/modified", Entry: tracked(Managed), State: Modified},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Status() = %#v, want %#v", got, want)
	}
	for _, tt := range []struct {
		state FileState
		want  string
	}{{Unchanged, "unchanged"}, {Modified, "modified"}, {Missing, "missing"}, {FileState(99), "FileState(99)"}} {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}

func TestTombstoneRestoreSetFile(t *testing.T) {
	t.Parallel()
	l := New()
	l.Files["a"] = tracked(Scaffold)
	l.Tombstone("a")
	l.Tombstone("a")
	l.Tombstone("z")
	if _, ok := l.Files["a"]; ok || !reflect.DeepEqual(l.Tombstones, []string{"a", "z"}) {
		t.Fatalf("Tombstone() = %#v", l)
	}
	l.Restore("a")
	l.Restore("a")
	if !reflect.DeepEqual(l.Tombstones, []string{"z"}) || len(l.Files) != 0 {
		t.Fatalf("Restore() = %#v", l)
	}
	if err := l.SetFile("z", tracked(Managed)); err != nil {
		t.Fatal(err)
	}
	if len(l.Tombstones) != 0 || l.Files["z"] != tracked(Managed) {
		t.Fatalf("SetFile() = %#v", l)
	}
	l.Tombstone("bad")
	if err := l.SetFile("bad", FileEntry{Class: "other"}); err == nil {
		t.Fatal("SetFile accepted an invalid entry")
	}
	if _, ok := l.Files["bad"]; ok || !reflect.DeepEqual(l.Tombstones, []string{"bad"}) {
		t.Fatal("invalid SetFile changed the lock")
	}
	if err := l.SetFile("../bad", tracked(Managed)); err == nil {
		t.Fatal("SetFile accepted traversal")
	}
	// A decoded or manually constructed lock may have a nil files map.
	l.Files = nil
	if err := l.SetFile("seed", FileEntry{Class: Seed}); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileMissing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	l := New()
	for _, path := range []string{"z/scaffold", "a/scaffold", "present"} {
		l.Files[path] = tracked(Scaffold)
	}
	for _, path := range []string{"z/managed", "a/managed", "modified"} {
		l.Files[path] = tracked(Managed)
	}
	l.Files["seed"] = FileEntry{Class: Seed}
	writeFile(t, root, "present", "original")
	writeFile(t, root, "modified", "user change")
	tombstoned, managed, err := l.ReconcileMissing(root)
	if err != nil {
		t.Fatal(err)
	}
	wantTombstones := []string{"a/scaffold", "z/scaffold"}
	wantManaged := []string{"a/managed", "z/managed"}
	if !reflect.DeepEqual(tombstoned, wantTombstones) || !reflect.DeepEqual(managed, wantManaged) || !reflect.DeepEqual(l.Tombstones, wantTombstones) {
		t.Fatalf("ReconcileMissing() = %v, %v, lock = %#v", tombstoned, managed, l)
	}
	for _, path := range wantTombstones {
		if _, ok := l.Files[path]; ok {
			t.Errorf("tombstoned file %q is still live", path)
		}
	}
	for _, path := range append(wantManaged, "seed", "present", "modified") {
		if _, ok := l.Files[path]; !ok {
			t.Errorf("file %q was removed", path)
		}
	}
	tombstoned, managed, err = l.ReconcileMissing(root)
	if err != nil || len(tombstoned) != 0 || !reflect.DeepEqual(managed, wantManaged) {
		t.Fatalf("second reconciliation = %v, %v, %v", tombstoned, managed, err)
	}
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDiskErrors(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		init func(*testing.T, string, *Lock)
	}{
		{"directory", func(t *testing.T, root string, l *Lock) {
			if err := os.Mkdir(filepath.Join(root, "a"), 0755); err != nil {
				t.Fatal(err)
			}
		}},
		{"invalid path", func(_ *testing.T, _ string, l *Lock) { l.Files["../outside"] = tracked(Scaffold) }},
		{"outside symlink", func(t *testing.T, root string, l *Lock) {
			outside := t.TempDir()
			writeFile(t, outside, "a", "original")
			if err := os.Symlink(filepath.Join(outside, "a"), filepath.Join(root, "a")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			l := New()
			l.Files["a"] = tracked(Managed)
			l.Files["0-missing-scaffold"] = tracked(Scaffold)
			tt.init(t, root, l)
			if _, err := l.Status(root); err == nil || !strings.Contains(err.Error(), "\nTry:") {
				t.Fatalf("Status() error = %v", err)
			}
			if _, _, err := l.ReconcileMissing(root); err == nil || !strings.Contains(err.Error(), "\nTry:") {
				t.Fatalf("ReconcileMissing() error = %v", err)
			}
			if len(l.Tombstones) != 0 {
				t.Fatal("failed reconciliation changed the lock")
			}
		})
	}
	l := New()
	l.Files["a"] = tracked(Managed)
	root := filepath.Join(t.TempDir(), "absent")
	if _, err := l.Status(root); err == nil {
		t.Fatal("Status accepted a missing root")
	}
	if _, _, err := l.ReconcileMissing(root); err == nil {
		t.Fatal("ReconcileMissing accepted a missing root")
	}
}
