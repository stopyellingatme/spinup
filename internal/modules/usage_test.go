package modules

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestEnabledNames guards the programming contract for every production call site.
func TestEnabledNames(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "internal")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || filepath.Dir(path) == filepath.Join(root, "modules") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		alias := ""
		for _, imp := range file.Imports {
			if imp.Path.Value == `"github.com/stopyellingatme/spinup/internal/modules"` {
				alias = "modules"
				if imp.Name != nil {
					alias = imp.Name.Name
				}
			}
		}
		if alias == "" {
			return nil
		}
		constants := map[string]string{}
		for _, decl := range file.Decls {
			g, ok := decl.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, spec := range g.Specs {
				v := spec.(*ast.ValueSpec)
				for i, name := range v.Names {
					if i < len(v.Values) {
						if lit, ok := v.Values[i].(*ast.BasicLit); ok {
							constants[name.Name], _ = strconv.Unquote(lit.Value)
						}
					}
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Enabled" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != alias || len(call.Args) != 2 {
				return true
			}
			name := ""
			switch arg := call.Args[1].(type) {
			case *ast.BasicLit:
				name, _ = strconv.Unquote(arg.Value)
			case *ast.Ident:
				name = constants[arg.Name]
			}
			if _, ok := Get(name); !ok {
				t.Errorf("%s: Enabled uses unknown module %q", path, name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
