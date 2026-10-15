package apicompat

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
)

// deprecationForm is the required form of a Deprecated line. The replacement
// can be "Use X instead." or "No replacement."
var deprecationForm = regexp.MustCompile(`^Deprecated: since v0\.\d+\. (Use \S.* instead|No replacement)\.$`)

// Problem is one deprecation comment that breaks the policy.
type Problem struct {
	Pos     string
	Message string
}

func (p Problem) String() string { return p.Pos + ": " + p.Message }

// CheckDeprecations scans the non-test Go files under root. Every line that
// starts with "Deprecated:" in a doc comment must have the policy form. A
// deprecated function whose body is one return or call statement must also
// have a `//go:fix inline` directive.
func CheckDeprecations(root string) ([]Problem, error) {
	var problems []Problem
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parse %s: %w\nTry: fix the syntax error", path, err)
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				problems = append(problems, checkDoc(fset, d.Doc, d)...)
			case *ast.GenDecl:
				problems = append(problems, checkDoc(fset, d.Doc, nil)...)
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						problems = append(problems, checkDoc(fset, s.Doc, nil)...)
					case *ast.ValueSpec:
						problems = append(problems, checkDoc(fset, s.Doc, nil)...)
					}
				}
			}
		}
		return nil
	})
	return problems, err
}

func checkDoc(fset *token.FileSet, doc *ast.CommentGroup, fn *ast.FuncDecl) []Problem {
	if doc == nil {
		return nil
	}
	var problems []Problem
	deprecated, inline := false, false
	for _, c := range doc.List {
		text := c.Text
		if strings.TrimSpace(text) == "//go:fix inline" {
			inline = true
			continue
		}
		line := strings.TrimPrefix(strings.TrimPrefix(text, "//"), " ")
		if !strings.HasPrefix(line, "Deprecated:") {
			continue
		}
		deprecated = true
		if !deprecationForm.MatchString(strings.TrimRight(line, " ")) {
			problems = append(problems, Problem{
				Pos:     fset.Position(c.Pos()).String(),
				Message: fmt.Sprintf("deprecation comment %q has the wrong form\nTry: write `// Deprecated: since v0.N. Use X instead.` on one line", line),
			})
		}
	}
	if deprecated && !inline && fn != nil && singleCall(fn) {
		problems = append(problems, Problem{
			Pos:     fset.Position(fn.Pos()).String(),
			Message: fmt.Sprintf("deprecated function %s forwards to another call but has no //go:fix inline directive\nTry: add `//go:fix inline` as the last line of its doc comment", fn.Name.Name),
		})
	}
	return problems
}

// singleCall reports whether the body is one return or expression statement
// that holds a call to another function.
func singleCall(fn *ast.FuncDecl) bool {
	if fn.Body == nil || len(fn.Body.List) != 1 {
		return false
	}
	switch s := fn.Body.List[0].(type) {
	case *ast.ReturnStmt:
		return len(s.Results) == 1 && isCall(s.Results[0])
	case *ast.ExprStmt:
		return isCall(s.X)
	}
	return false
}

func isCall(e ast.Expr) bool {
	_, ok := e.(*ast.CallExpr)
	return ok
}
