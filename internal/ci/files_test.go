package ci_test

import (
	"slices"
	"testing"

	"github.com/stopyellingatme/spinup/internal/ci"
)

func TestManagedFiles(t *testing.T) {
	t.Parallel()
	files := ci.ManagedFiles()
	if len(files) != 5 || !slices.IsSorted(files) || len(slices.Compact(slices.Clone(files))) != len(files) {
		t.Fatalf("invalid managed paths: %v", files)
	}
	files[0] = "changed"
	if ci.ManagedFiles()[0] == "changed" {
		t.Fatal("caller changed shared state")
	}
}
