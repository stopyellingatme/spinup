package cli

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
)

func blankImports(t *testing.T, path string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, spec := range file.Imports {
		if spec.Name == nil || spec.Name.Name != "_" {
			continue
		}
		value, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, value)
	}
	sort.Strings(paths)
	return paths
}

// TestBlankImportsMatchWireGen keeps the docs site build in step with the binary.
func TestBlankImportsMatchWireGen(t *testing.T) {
	want := blankImports(t, filepath.Join(repoRoot(t), "cmd", "spinup", "wire_gen.go"))
	got := blankImports(t, "wire_imports_test.go")
	if len(want) == 0 {
		t.Fatal("wire_gen.go has no blank imports")
	}
	have := map[string]bool{}
	for _, path := range got {
		have[path] = true
	}
	wanted := map[string]bool{}
	for _, path := range want {
		wanted[path] = true
		if !have[path] {
			t.Errorf("wire_imports_test.go lacks the blank import %q. Try: add it to internal/cli/wire_imports_test.go", path)
		}
	}
	for _, path := range got {
		if !wanted[path] {
			t.Errorf("wire_imports_test.go has the extra blank import %q. Try: remove it or add it to cmd/spinup/wire_gen.go", path)
		}
	}
}
