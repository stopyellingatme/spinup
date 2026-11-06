package vfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompareSkipsUnifiedDiff(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var plan Plan
	if err := plan.Add(File{Path: "a.txt", Data: []byte("new\n"), Mode: 0o644}); err != nil {
		t.Fatal(err)
	}
	withDiff, err := Diff(root, plan)
	if err != nil {
		t.Fatal(err)
	}
	without, err := Compare(root, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(withDiff) != 1 || len(without) != 1 {
		t.Fatalf("changes: diff %d, compare %d", len(withDiff), len(without))
	}
	if withDiff[0].UnifiedDiff == "" {
		t.Fatal("Diff left UnifiedDiff empty")
	}
	if without[0].UnifiedDiff != "" {
		t.Fatalf("Compare computed a diff: %q", without[0].UnifiedDiff)
	}
	if withDiff[0].Kind != without[0].Kind || withDiff[0].Path != without[0].Path {
		t.Fatalf("Compare and Diff disagree: %+v %+v", withDiff[0], without[0])
	}
}
