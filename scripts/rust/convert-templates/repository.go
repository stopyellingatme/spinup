package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type inventory struct {
	PackTemplates   int      `json:"pack_templates"`
	InlineTemplates []string `json:"inline_templates"`
	Clashes         []clash  `json:"clashes"`
	DynamicSites    []string `json:"dynamic_sites"`
}

func writeFile(name string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return err
	}
	return os.WriteFile(name, data, 0644)
}

func convertRepository(root, out string) error {
	var inv inventory
	packs := filepath.Join(root, "internal/templates/packs")
	err := filepath.WalkDir(packs, func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(packs, name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if strings.HasSuffix(rel, ".tmpl") {
			inv.PackTemplates++
		}
		data, clashes, err := convertFile(filepath.ToSlash(rel), data)
		if err != nil {
			return err
		}
		inv.Clashes = append(inv.Clashes, clashes...)
		return writeFile(filepath.Join(out, rel), data)
	})
	if err != nil {
		return err
	}
	if err := convertInline(root, out, &inv); err != nil {
		return err
	}
	inv.DynamicSites = []string{
		"internal/plugin/render/generator.go: execute(source, data), including generatorPath",
		"internal/plugin/pack/meta.go: checkTemplatePath(value, args)",
		"internal/render/render.go: execute(name, source, data, funcs), pack contents and FileSpec.Path",
	}
	if err := writeJSON(filepath.Join(filepath.Dir(out), "fixtures/inventory.json"), inv); err != nil {
		return err
	}
	fmt.Printf("converted %d pack templates and %d inline templates\n", inv.PackTemplates, len(inv.InlineTemplates))
	return nil
}

// convertFile converts one pack file. rel is the slash-separated path below the packs
// directory. A .tmpl file is a template. In a .toml file, only the `path = ` lines are
// templates. Other files stay as they are.
func convertFile(rel string, data []byte) ([]byte, []clash, error) {
	if strings.HasSuffix(rel, ".tmpl") {
		converted, clashes, err := convert(rel, string(data), "[[", "]]")
		if err != nil {
			return nil, nil, err
		}
		return []byte(converted), clashes, nil
	}
	if !strings.HasSuffix(rel, ".toml") {
		return data, nil, nil
	}
	// Metadata has literal TOML arrays. Only output paths are templates.
	var all []clash
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "path = ") {
			continue
		}
		var metadata struct{ Path string }
		if err := toml.Unmarshal([]byte(line), &metadata); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", rel, err)
		}
		converted, clashes, err := convert(rel+" path", metadata.Path, "[[", "]]")
		if err != nil {
			return nil, nil, err
		}
		all = append(all, clashes...)
		lines[i] = "path = " + strconv.Quote(converted)
	}
	return []byte(strings.Join(lines, "\n")), all, nil
}

// stringEvaluator reads composed inline strings without executing generator code.
// It accepts literals, declarations, concatenations, and a single-return helper.
type stringEvaluator struct {
	values map[string]ast.Expr
	funcs  map[string]*ast.FuncDecl
}

func readEvaluator(dir string) (*stringEvaluator, error) {
	e := &stringEvaluator{values: map[string]ast.Expr{}, funcs: map[string]*ast.FuncDecl{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, entry.Name()), nil, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					v, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, value := range v.Values {
						if i < len(v.Names) {
							e.values[v.Names[i].Name] = value
						}
					}
				}
			case *ast.FuncDecl:
				e.funcs[d.Name.Name] = d
			}
		}
	}
	return e, nil
}

func (e *stringEvaluator) eval(expr ast.Expr, bindings map[string]string) (string, error) {
	switch n := expr.(type) {
	case *ast.BasicLit:
		return strconv.Unquote(n.Value)
	case *ast.Ident:
		if value, ok := bindings[n.Name]; ok {
			return value, nil
		}
		if value, ok := e.values[n.Name]; ok {
			return e.eval(value, bindings)
		}
	case *ast.BinaryExpr:
		if n.Op != token.ADD {
			break
		}
		left, err := e.eval(n.X, bindings)
		if err != nil {
			return "", err
		}
		right, err := e.eval(n.Y, bindings)
		return left + right, err
	case *ast.CallExpr:
		// Job templates wrap the string in template.Must(...Parse(...)).
		if selector, ok := n.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Parse" && len(n.Args) == 1 {
			return e.eval(n.Args[0], bindings)
		}
		if selector, ok := n.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "Must" && len(n.Args) == 1 {
			return e.eval(n.Args[0], bindings)
		}
		if id, ok := n.Fun.(*ast.Ident); ok {
			f := e.funcs[id.Name]
			if f == nil || f.Body == nil || len(f.Body.List) != 1 {
				break
			}
			ret, ok := f.Body.List[0].(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				break
			}
			args := map[string]string{}
			i := 0
			for _, field := range f.Type.Params.List {
				for _, name := range field.Names {
					if i >= len(n.Args) {
						return "", fmt.Errorf("missing argument for %s", id.Name)
					}
					value, err := e.eval(n.Args[i], bindings)
					if err != nil {
						return "", err
					}
					args[name.Name] = value
					i++
				}
			}
			return e.eval(ret.Results[0], args)
		}
	}
	return "", fmt.Errorf("unsupported inline expression %T", expr)
}

func convertInline(root, out string, inv *inventory) error {
	for _, group := range []struct{ dir, prefix, names, left, right string }{
		{"internal/gen/job", "job", "registerTemplate jobTemplate testTemplate", "{{", "}}"},
		{"internal/gen/resource", "resource", "listServerTS listSvelte itemServerTS itemSvelte editServerTS editSvelte fieldsSvelte newServerTS newSvelte schemaTS", "[[", "]]"},
	} {
		e, err := readEvaluator(filepath.Join(root, group.dir))
		if err != nil {
			return err
		}
		for _, name := range strings.Fields(group.names) {
			expr, ok := e.values[name]
			if !ok {
				return fmt.Errorf("missing inline template %s/%s", group.dir, name)
			}
			source, err := e.eval(expr, nil)
			if err != nil {
				return err
			}
			path := "_inline/" + group.prefix + "/" + name + ".tmpl"
			converted, clashes, err := convert(path, source, group.left, group.right)
			if err != nil {
				return err
			}
			inv.InlineTemplates = append(inv.InlineTemplates, path)
			inv.Clashes = append(inv.Clashes, clashes...)
			if err := writeFile(filepath.Join(out, filepath.FromSlash(path)), []byte(converted)); err != nil {
				return err
			}
		}
	}
	dir := filepath.Join(root, "internal/plugin/scaffold/templates")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		source, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return err
		}
		path := "_inline/plugin-scaffold/" + strings.TrimSuffix(entry.Name(), ".txt") + ".tmpl"
		converted, clashes, err := convert(path, string(source), "<%", "%>")
		if err != nil {
			return err
		}
		inv.InlineTemplates = append(inv.InlineTemplates, path)
		inv.Clashes = append(inv.Clashes, clashes...)
		if err := writeFile(filepath.Join(out, filepath.FromSlash(path)), []byte(converted)); err != nil {
			return err
		}
	}
	return nil
}
