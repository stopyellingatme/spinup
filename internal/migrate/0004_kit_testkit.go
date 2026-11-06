package migrate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"

	"github.com/stopyellingatme/spinup/internal/exec"
)

// KitTestkitID identifies the codemod that adds kit migrations to generated tests.
const KitTestkitID = "0004_kit_testkit"

func init() { Register(kitTestkit()) }

type kitTestEdit struct {
	path string
	data []byte
	mode fs.FileMode
}

func kitTestkit() Migration {
	return Migration{
		ID:    KitTestkitID,
		Since: "0.1.0-alpha.419",
		Desc:  "Include kit migrations in generated test databases",
		Stage: After,
		Plan: func(c *Ctx) ([]Change, error) {
			edits, manual, err := kitTestEdits(c)
			changes := make([]Change, 0, len(edits)+len(manual))
			for _, edit := range edits {
				changes = append(changes, Change{Path: edit.path, Desc: "Include kit migrations in the test database"})
			}
			return append(changes, manual...), err
		},
		Apply: func(c *Ctx) error {
			edits, _, err := kitTestEdits(c)
			if err != nil {
				return err
			}
			root, err := os.OpenRoot(c.Root)
			if err != nil {
				return kitTestError("api", err)
			}
			defer root.Close()
			for _, edit := range edits {
				if err := root.WriteFile(edit.path, edit.data, edit.mode); err != nil {
					return kitTestError(edit.path, err)
				}
			}
			return nil
		},
	}
}

func kitTestEdits(c *Ctx) ([]kitTestEdit, []Change, error) {
	root, err := os.OpenRoot(c.Root)
	if err != nil {
		return nil, nil, kitTestError("api", err)
	}
	defer root.Close()
	// Apps without auth have no kit migrations.
	if _, err := root.Stat("api/db/kit"); errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	} else if err != nil {
		return nil, nil, kitTestError("api/db/kit", err)
	}
	var edits []kitTestEdit
	var manual []Change
	var failures []error
	err = fs.WalkDir(root.FS(), "api", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := root.ReadFile(path)
		if err != nil {
			return err
		}
		data, changed, err := rewriteKitTest(c, path, source)
		if err != nil {
			manual = append(manual, Change{Path: path, Desc: "Manual step: " + path + " needs kit migrations in its test database"})
			failures = append(failures, err)
			return nil
		}
		if changed {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s is not a regular file", path)
			}
			edits = append(edits, kitTestEdit{path: path, data: data, mode: info.Mode().Perm()})
		}
		return nil
	})
	if err != nil {
		failures = append(failures, kitTestError("api", err))
	}
	return edits, manual, errors.Join(failures...)
}

func rewriteKitTest(c *Ctx, path string, source []byte) ([]byte, bool, error) {
	fset := token.NewFileSet()
	file, parseErr := parser.ParseFile(fset, path, source, parser.ParseComments)
	if file == nil {
		return nil, false, nil
	}
	var calls []*ast.CallExpr
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 || call.Ellipsis.IsValid() || !kitTestSelector(call.Fun, "testkit", "DBWithMigrations") || !kitTestSelector(call.Args[1], "migrations", "FS") {
			return true
		}
		if arg, ok := call.Args[0].(*ast.Ident); ok && arg.Name == "t" {
			calls = append(calls, call)
		}
		return true
	})
	if len(calls) == 0 {
		return nil, false, nil
	}
	if parseErr != nil {
		return nil, false, kitTestError(path, parseErr)
	}
	var kitPath, alias string
	for _, spec := range file.Imports {
		value, _ := strconv.Unquote(spec.Path.Value)
		name := "migrations"
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name == "migrations" && strings.HasSuffix(value, "/api/db/migrations") {
			kitPath = strings.TrimSuffix(value, "migrations") + "kit"
		}
	}
	if kitPath == "" {
		return nil, false, kitTestError(path, errors.New("the migrations import is not a generated app import"))
	}
	alias = "kitmigrations"
	hasKitImport := false
	for _, spec := range file.Imports {
		value, _ := strconv.Unquote(spec.Path.Value)
		if value == kitPath {
			hasKitImport = true
			alias = "kit"
			if spec.Name != nil {
				alias = spec.Name.Name
			}
			if alias == "." || alias == "_" {
				return nil, false, kitTestError(path, errors.New("the kit import has no usable name"))
			}
		}
	}
	// Insert arguments in source order before parsing again. New AST nodes without
	// positions can move a trailing comment into the middle of the added argument.
	for i := len(calls) - 1; i >= 0; i-- {
		offset := fset.Position(calls[i].Args[1].End()).Offset
		argument := ", testkit.WithKitMigrations(" + alias + ".FS)"
		updated := make([]byte, 0, len(source)+len(argument))
		updated = append(updated, source[:offset]...)
		updated = append(updated, argument...)
		source = append(updated, source[offset:]...)
	}
	fset = token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, parser.ParseComments)
	if err != nil {
		return nil, false, kitTestError(path, err)
	}
	if !hasKitImport {
		astutil.AddNamedImport(fset, file, alias, kitPath)
	}
	var output bytes.Buffer
	if err := printer.Fprint(&output, fset, file); err != nil {
		return nil, false, kitTestError(path, err)
	}
	if c.Runner == nil {
		return nil, false, kitTestError(path, errors.New("no command runner for gofmt"))
	}
	result, err := c.Runner.Run(context.Background(), exec.Cmd{Name: "gofmt", Dir: c.Root, Stdin: &output})
	if err != nil {
		return nil, false, kitTestError(path, err)
	}
	return result.Stdout, true, nil
}

func kitTestSelector(expr ast.Expr, name, field string) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != field {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	return ok && ident.Name == name
}

func kitTestError(path string, err error) error {
	return fmt.Errorf("rewrite kit test %s: %w\nTry: add testkit.WithKitMigrations(kitmigrations.FS) and the kitmigrations import by hand, then run gofmt and spinup upgrade again", path, err)
}
