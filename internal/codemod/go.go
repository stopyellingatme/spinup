package codemod

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
)

// EditGo parses comments, applies edit, and formats a changed Go file.
// If edit reports no change, EditGo returns src unchanged.
func EditGo(filename string, src []byte, edit func(*token.FileSet, *ast.File) (bool, error)) ([]byte, bool, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, false, editorError("Go", err)
	}
	changed, err := edit(fset, f)
	if err != nil {
		return nil, false, editorError("Go", err)
	}
	if !changed {
		return src, false, nil
	}
	var out bytes.Buffer
	if err := format.Node(&out, fset, f); err != nil {
		return nil, false, editorError("Go", err)
	}
	return out.Bytes(), !bytes.Equal(src, out.Bytes()), nil
}

// AddImport adds importPath and reports whether the file changed.
func AddImport(fset *token.FileSet, f *ast.File, importPath string) bool {
	return astutil.AddImport(fset, f, importPath)
}

// DeleteImport deletes importPath and reports whether the file changed.
func DeleteImport(fset *token.FileSet, f *ast.File, importPath string) bool {
	comments := append([]*ast.CommentGroup(nil), f.Comments...)
	var names []string
	for _, spec := range f.Imports {
		imported, err := strconv.Unquote(spec.Path.Value)
		if err == nil && imported == importPath {
			name := ""
			if spec.Name != nil {
				name = spec.Name.Name
			}
			names = append(names, name)
		}
	}
	changed := false
	for _, name := range names {
		if astutil.DeleteNamedImport(fset, f, name, importPath) {
			changed = true
		}
	}
	// astutil removes comments attached to deleted imports. Keep them as free comments.
	f.Comments = comments
	return changed
}

// RewriteImport replaces oldPath with newPath and reports whether it changed.
func RewriteImport(fset *token.FileSet, f *ast.File, oldPath, newPath string) bool {
	if oldPath == newPath {
		return false
	}
	return astutil.RewriteImport(fset, f, oldPath, newPath)
}

// RenameSelector renames references to an imported selector, including aliases.
// It skips dot imports, blank imports, and identifiers that shadow the import.
func RenameSelector(f *ast.File, importPath, oldName, newName string) bool {
	if oldName == newName {
		return false
	}
	local := ""
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil || p != importPath {
			continue
		}
		local = importLocalName(importPath)
		if spec.Name != nil {
			local = spec.Name.Name
		}
		break
	}
	if local == "" || local == "." || local == "_" {
		return false
	}
	changed := false
	ast.Inspect(f, func(n ast.Node) bool {
		s, ok := n.(*ast.SelectorExpr)
		if !ok || s.Sel.Name != oldName {
			return true
		}
		id, ok := s.X.(*ast.Ident)
		if ok && id.Name == local && id.Obj == nil {
			s.Sel.Name = newName
			changed = true
		}
		return true
	})
	return changed
}

func editorError(kind string, err error) error {
	return fmt.Errorf("edit %s: %w\nTry: check the document and the migration input", kind, err)
}

func importLocalName(importPath string) string {
	name := path.Base(importPath)
	if strings.HasPrefix(name, "v") {
		if version, err := strconv.Atoi(name[1:]); err == nil && version > 0 {
			name = path.Base(path.Dir(importPath))
		}
	}
	if index := strings.LastIndex(name, ".v"); index >= 0 {
		if version, err := strconv.Atoi(name[index+2:]); err == nil && version > 0 {
			name = name[:index]
		}
	}
	return name
}
