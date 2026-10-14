package testutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/render"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
)

// RepoRoot finds the spinup module root above the test's working directory.
func RepoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("find repository root: %v\nTry: run tests from the repository", err)
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 2 && fields[0] == "module" && strings.Trim(fields[1], `"`) == "github.com/stopyellingatme/spinup" {
					return dir
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("find repository root: spinup go.mod was not found\nTry: run tests from the repository")
			return ""
		}
		dir = parent
	}
}

// GoldenSelected skips a golden test when another pack was selected.
func GoldenSelected(t testing.TB, name string) bool {
	t.Helper()
	if selected := os.Getenv("SPINUP_GOLDEN_PACK"); selected != "" && selected != name {
		t.Skipf("golden pack %q was not selected (selected %q)", name, selected)
		return false
	}
	return true
}

// Golden compares file bytes and modes with the named golden tree.
// SPINUP_GOLDEN_UPDATE=1 replaces the expected tree, including stale files.
func Golden(t testing.TB, name string, got fs.FS) {
	t.Helper()
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\\x00") {
		t.Fatalf("compare golden: invalid name %q\nTry: use a relative pack path", name)
	}
	files, err := readTree(got)
	if err != nil {
		t.Fatalf("read generated tree: %v\nTry: return readable regular files", err)
	}
	if _, exists := files[".modes"]; exists {
		t.Fatal("compare golden: .modes is reserved\nTry: use another output path")
	}
	dir := filepath.Join(RepoRoot(t), "testdata", "golden", filepath.FromSlash(name))
	if os.Getenv("SPINUP_GOLDEN_UPDATE") == "1" {
		if err := writeGolden(dir, files); err != nil {
			t.Fatalf("update golden: %v\nTry: make the golden directory writable", err)
		}
		t.Logf("updated golden tree %s", dir)
		return
	}
	want, err := readGolden(dir)
	if err != nil {
		t.Fatalf("read golden %s: %v\nTry: run make golden PACK=%s UPDATE=1", name, err, name)
	}
	for _, report := range compareTrees(want, files) {
		t.Errorf("golden %s: %s\nTry: inspect the render or run make golden PACK=%s UPDATE=1", name, report, name)
	}
}

// Deterministic renders twice and compares every file's bytes and mode.
func Deterministic(t testing.TB, render func() (fs.FS, error)) {
	t.Helper()
	var trees [2]tree
	for i := range trees {
		got, err := render()
		if err != nil {
			t.Fatalf("determinism render %d: %v\nTry: correct the render input", i+1, err)
		}
		trees[i], err = readTree(got)
		if err != nil {
			t.Fatalf("read determinism render %d: %v\nTry: return readable regular files", i+1, err)
		}
	}
	for _, report := range compareTrees(trees[0], trees[1]) {
		t.Errorf("render is not deterministic: %s\nTry: remove variable inputs and sort output keys", report)
	}
}

// RenderPack loads embedded packs, validates answers, and renders test output.
func RenderPack(t testing.TB, packs []string, answers map[string]any) fs.FS {
	t.Helper()
	loaded := make([]*templates.Pack, 0, len(packs))
	for _, name := range packs {
		pack, err := templates.Load(templates.Embedded(), name)
		if err != nil {
			t.Fatalf("load test pack %q: %v", name, err)
		}
		loaded = append(loaded, pack)
	}
	validated, err := render.ValidateAnswers(loaded, answers)
	if err != nil {
		t.Fatalf("validate test answers: %v", err)
	}
	plan, err := render.Render(render.Input{Packs: loaded, Answers: validated, Stack: stackversions.MustLoad(), TemplateVersion: "0.0.0-test"})
	if err != nil {
		t.Fatal(fmt.Errorf("render test packs: %w", err))
	}
	return plan.FS()
}
