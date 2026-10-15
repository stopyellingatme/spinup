// Package apicompat extracts the exported API of the kit and compares it with
// a committed baseline. It also checks the form of deprecation comments.
// It uses only the standard library and reads source without type checking.
package apicompat

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// excluded lists kit path elements that are not part of the stable API.
// "internal" and "x" are unstable by policy. "devtools" serves the /_dev page
// and the package doc says the kit compiles it out unless SPINUP_ENV=dev.
var excluded = map[string]bool{"internal": true, "x": true, "devtools": true, "testdata": true, "api": true}

var deprecatedMarker = regexp.MustCompile(`(?m)^Deprecated: since v0\.(\d+)\.`)

// Extract returns the sorted exported API lines of every stable package under
// kitDir. Package paths in the lines are relative to kitDir and start with
// "kit". Each line has the form "<pkg> <kind> <name><signature>" and can end
// with " deprecated:v0.N".
func Extract(kitDir string) ([]string, error) {
	dirs := map[string][]string{}
	err := filepath.WalkDir(kitDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(kitDir, path)
		if entry.IsDir() {
			if rel != "." && (excluded[entry.Name()] || strings.HasPrefix(entry.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			dirs[filepath.Dir(rel)] = append(dirs[filepath.Dir(rel)], path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for dir, files := range dirs {
		pkg := "kit"
		if dir != "." {
			pkg += "/" + filepath.ToSlash(dir)
		}
		for _, file := range files {
			if err := extractFile(file, pkg, set); err != nil {
				return nil, err
			}
		}
	}
	lines := make([]string, 0, len(set))
	for line := range set {
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return lines, nil
}

func extractFile(path, pkg string, set map[string]bool) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("parse %s: %w\nTry: fix the syntax error in the kit source", path, err)
	}
	add := func(kind, name, sig string, doc ...*ast.CommentGroup) {
		line := pkg + " " + kind + " " + name + sig
		for _, group := range doc {
			if m := deprecatedMarker.FindStringSubmatch(group.Text()); m != nil {
				line += " deprecated:v0." + m[1]
				break
			}
		}
		set[line] = true
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if !d.Name.IsExported() {
				continue
			}
			sig := typeParams(d.Type.TypeParams) + funcSig(d.Type)
			if d.Recv == nil {
				add("func", d.Name.Name, sig, d.Doc)
				continue
			}
			recv, ptr, ok := receiver(d.Recv.List[0].Type)
			if !ok {
				continue
			}
			kind := "method"
			if ptr {
				kind = "ptrmethod"
			}
			add(kind, recv+"."+d.Name.Name, sig, d.Doc)
		case *ast.GenDecl:
			extractGen(d, add)
		}
	}
	return nil
}

func extractGen(d *ast.GenDecl, add func(kind, name, sig string, doc ...*ast.CommentGroup)) {
	var prevType ast.Expr
	var prevValues []ast.Expr
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if !s.Name.IsExported() {
				continue
			}
			name := s.Name.Name + typeParams(s.TypeParams)
			if s.Assign.IsValid() {
				add("type", name, " = "+expr(s.Type), d.Doc, s.Doc)
				continue
			}
			switch t := s.Type.(type) {
			case *ast.StructType:
				add("type", name, " struct", d.Doc, s.Doc)
				for _, field := range t.Fields.List {
					for _, n := range fieldNames(field) {
						if ast.IsExported(n) || (len(field.Names) == 0 && exportedEmbedded(field.Type)) {
							kind := "field"
							add(kind, s.Name.Name+"."+n, " "+expr(field.Type), field.Doc)
						}
					}
				}
			case *ast.InterfaceType:
				add("type", name, " interface", d.Doc, s.Doc)
				for _, m := range t.Methods.List {
					if len(m.Names) == 0 {
						add("imethod", s.Name.Name+".embed", " "+expr(m.Type), m.Doc)
						continue
					}
					if ft, ok := m.Type.(*ast.FuncType); ok && m.Names[0].IsExported() {
						add("imethod", s.Name.Name+"."+m.Names[0].Name, funcSig(ft), m.Doc)
					}
				}
			default:
				add("type", name, " "+expr(s.Type), d.Doc, s.Doc)
			}
		case *ast.ValueSpec:
			kind := "var"
			typ, values := s.Type, s.Values
			iotaIndex := -1
			if d.Tok == token.CONST {
				kind = "const"
				if typ == nil && len(values) == 0 {
					typ, values = prevType, prevValues
					iotaIndex = 0
				} else {
					prevType, prevValues = typ, values
				}
			}
			for i, n := range s.Names {
				if !n.IsExported() {
					continue
				}
				sig := ""
				if typ != nil {
					sig += " " + expr(typ)
				}
				if i < len(values) && (kind == "const" || typ == nil) {
					sig += " = " + expr(values[i])
					if iotaIndex >= 0 {
						sig += " @" + fmt.Sprint(constOffset(d, s))
					}
				}
				add(kind, n.Name, sig, d.Doc, s.Doc)
			}
		}
	}
}

// constOffset returns the position of s among the specs of its const group.
func constOffset(d *ast.GenDecl, s *ast.ValueSpec) int {
	for i, spec := range d.Specs {
		if spec == ast.Spec(s) {
			return i
		}
	}
	return -1
}

func fieldNames(f *ast.Field) []string {
	if len(f.Names) == 0 {
		return []string{"embed"}
	}
	names := make([]string, len(f.Names))
	for i, n := range f.Names {
		names[i] = n.Name
	}
	return names
}

func exportedEmbedded(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.StarExpr:
		return exportedEmbedded(t.X)
	case *ast.Ident:
		return t.IsExported()
	case *ast.SelectorExpr:
		return t.Sel.IsExported()
	case *ast.IndexExpr:
		return exportedEmbedded(t.X)
	case *ast.IndexListExpr:
		return exportedEmbedded(t.X)
	}
	return false
}

func receiver(e ast.Expr) (name string, ptr, ok bool) {
	if star, isStar := e.(*ast.StarExpr); isStar {
		ptr = true
		e = star.X
	}
	switch t := e.(type) {
	case *ast.IndexExpr:
		e = t.X
	case *ast.IndexListExpr:
		e = t.X
	}
	id, isIdent := e.(*ast.Ident)
	if !isIdent || !id.IsExported() {
		return "", false, false
	}
	return id.Name, ptr, true
}

func typeParams(list *ast.FieldList) string {
	if list == nil || len(list.List) == 0 {
		return ""
	}
	return "[" + fieldList(list, true) + "]"
}

func funcSig(ft *ast.FuncType) string {
	sig := "(" + fieldList(ft.Params, false) + ")"
	if ft.Results != nil && len(ft.Results.List) > 0 {
		res := fieldList(ft.Results, false)
		if len(ft.Results.List) == 1 && len(ft.Results.List[0].Names) <= 1 {
			sig += " " + res
		} else {
			sig += " (" + res + ")"
		}
	}
	return sig
}

// fieldList prints a parameter, result, or type parameter list. Parameter and
// result names are not part of the API, so it drops them unless keepNames is set.
func fieldList(list *ast.FieldList, keepNames bool) string {
	if list == nil {
		return ""
	}
	parts := make([]string, 0, len(list.List))
	for _, f := range list.List {
		typ := expr(f.Type)
		if len(f.Names) == 0 {
			parts = append(parts, typ)
			continue
		}
		names := make([]string, len(f.Names))
		for i, n := range f.Names {
			names[i] = n.Name
		}
		if !keepNames {
			for range names {
				parts = append(parts, typ)
			}
			continue
		}
		parts = append(parts, strings.Join(names, ", ")+" "+typ)
	}
	return strings.Join(parts, ", ")
}

// expr prints an expression on one line. Struct and interface literals inside
// a type expression collapse to their kind and exported members.
func expr(e ast.Expr) string {
	return strings.Join(strings.Fields(types.ExprString(e)), " ")
}
