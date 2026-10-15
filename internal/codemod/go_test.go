package codemod

import (
	"bytes"
	"errors"
	"go/ast"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestGo(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("testdata/example.go.txt")
	if err != nil {
		t.Fatal(err)
	}
	same, changed, err := EditGo("example.go", src, func(*token.FileSet, *ast.File) (bool, error) { return false, nil })
	if err != nil || changed || !bytes.Equal(src, same) {
		t.Fatal("round trip", err)
	}
	edit := func(fset *token.FileSet, f *ast.File) (bool, error) {
		renamed := RenameSelector(f, "example.com/old", "Old", "New")
		rewritten := RewriteImport(fset, f, "example.com/old", "example.com/new")
		return renamed || rewritten, nil
	}
	out, changed, err := EditGo("example.go", src, edit)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	for _, comment := range []string{"File comment.", "Import comment.", "Function comment.", "Call comment.", "Shadow comment."} {
		if !bytes.Contains(out, []byte(comment)) {
			t.Fatal("lost comment", comment)
		}
	}
	if !bytes.Contains(out, []byte("alias.New()")) || !bytes.Contains(out, []byte("alias.Old() // Shadow")) {
		t.Fatal(string(out))
	}
	again, changed, err := EditGo("example.go", out, edit)
	if err != nil || changed || !bytes.Equal(out, again) {
		t.Fatal("not idempotent", changed, err)
	}
	for _, tt := range []struct {
		name string
		edit func(*token.FileSet, *ast.File) bool
	}{{"add", func(s *token.FileSet, f *ast.File) bool { return AddImport(s, f, "os") }}, {"delete", func(s *token.FileSet, f *ast.File) bool { return DeleteImport(s, f, "fmt") }}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, changed, err := EditGo("example.go", src, func(s *token.FileSet, f *ast.File) (bool, error) { return tt.edit(s, f), nil })
			if err != nil || !changed {
				t.Fatal(err)
			}
			again, changed, err := EditGo("example.go", out, func(s *token.FileSet, f *ast.File) (bool, error) { return tt.edit(s, f), nil })
			if err != nil || changed || !bytes.Equal(again, out) {
				t.Fatal("not idempotent", err)
			}
		})
	}
}
func TestGoErrors(t *testing.T) {
	t.Parallel()
	if _, _, err := EditGo("bad.go", []byte("bad"), func(*token.FileSet, *ast.File) (bool, error) { return false, nil }); err == nil {
		t.Fatal("accepted invalid Go")
	}
	want := errors.New("callback failed")
	_, _, err := EditGo("a.go", []byte("package a"), func(*token.FileSet, *ast.File) (bool, error) { return false, want })
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "Try:") {
		t.Fatal(err)
	}
}

func TestGoImportComments(t *testing.T) {
	t.Parallel()
	src := []byte("// File.\npackage p\n\nimport (\n // Docs.\n \"fmt\" // Inline.\n \"os\"\n)\n")
	out, _, err := EditGo("p.go", src, func(fset *token.FileSet, f *ast.File) (bool, error) { return DeleteImport(fset, f, "fmt"), nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, comment := range []string{"// File.", "// Docs.", "// Inline."} {
		if !bytes.Contains(out, []byte(comment)) {
			t.Fatalf("lost %s:\n%s", comment, out)
		}
	}
}

func TestGoVersionedImport(t *testing.T) {
	t.Parallel()
	for _, importPath := range []string{"example.com/pkg/v2", "gopkg.in/pkg.v2"} {
		src := []byte("package p\nimport \"" + importPath + "\"\nfunc f(){pkg.Old()}\n")
		out, changed, err := EditGo("p.go", src, func(_ *token.FileSet, f *ast.File) (bool, error) {
			return RenameSelector(f, importPath, "Old", "New"), nil
		})
		if err != nil || !changed || !bytes.Contains(out, []byte("pkg.New()")) {
			t.Fatalf("%s: %s (%v)", importPath, out, err)
		}
	}
}

func TestGoRenameSelectorWithPackageNameDifferentFromImportPath(t *testing.T) {
	t.Parallel()
	const importPath = "github.com/pelletier/go-toml/v2"
	src := []byte("package p\nimport \"" + importPath + "\"\nfunc f(){toml.Old()}\n")
	out, changed, err := EditGo("p.go", src, func(_ *token.FileSet, f *ast.File) (bool, error) {
		return RenameSelector(f, importPath, "Old", "New"), nil
	})
	if err != nil || !changed || !bytes.Contains(out, []byte("toml.New()")) {
		t.Fatalf("changed=%v, output=%s, err=%v", changed, out, err)
	}
}
