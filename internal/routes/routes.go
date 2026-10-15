// Package routes lists the HTTP routes of a generated application.
package routes

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"go.yaml.in/yaml/v3"
)

// Route describes an HTTP registration and its source.
type Route struct {
	// Method is the HTTP method, or * for all methods.
	Method string `json:"method"`
	// Path is the registered URL path.
	Path string `json:"path"`
	// Source identifies the kit, operation, or file and line.
	Source string `json:"source"`
}

// List reads built-ins, bundled OpenAPI operations, and constant mux registrations.
func List(root string) ([]Route, error) {
	result := []Route{{"GET", "/healthz", "kit"}, {"GET", "/readyz", "kit"}, {"GET", "/version", "kit"}}
	api, err := openAPI(filepath.Join(root, "api/openapi/bundle.gen.yaml"))
	if err != nil {
		return nil, err
	}
	result = append(result, api...)
	files, err := filepath.Glob(filepath.Join(root, "api/internal/routes/*.go"))
	if err != nil {
		return nil, routeError("find route files", err)
	}
	custom, err := goRoutes(files)
	if err != nil {
		return nil, err
	}
	result = append(result, custom...)
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Method != b.Method {
			return a.Method < b.Method
		}
		return a.Source < b.Source
	})
	return result, nil
}

func openAPI(path string) ([]Route, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, routeError("read OpenAPI bundle", err)
	}
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, routeError("parse OpenAPI bundle", err)
	}
	var result []Route
	for path, item := range doc.Paths {
		for _, method := range []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"} {
			node, ok := item[method]
			if !ok {
				continue
			}
			var operation struct {
				ID string `yaml:"operationId"`
			}
			if err := node.Decode(&operation); err != nil {
				return nil, routeError("parse OpenAPI operation", err)
			}
			result = append(result, Route{strings.ToUpper(method), path, "openapi:" + operation.ID})
		}
	}
	return result, nil
}

func goRoutes(paths []string) ([]Route, error) {
	fset := token.NewFileSet()
	var files []*ast.File
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, routeError("parse route file", err)
		}
		files = append(files, file)
	}
	// Type checking still evaluates local constants when unrelated imports or
	// handler identifiers are unresolved. No application dependencies are loaded.
	info := &types.Info{Types: make(map[ast.Expr]types.TypeAndValue)}
	config := types.Config{Error: func(error) {}}
	_, _ = config.Check("routes", fset, files, info)
	var result []Route
	for _, file := range files {
		filename := filepath.Base(fset.Position(file.Pos()).Filename)
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) < 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
				return true
			}
			receiver, ok := sel.X.(*ast.Ident)
			if !ok || receiver.Name != "mux" {
				return true
			}
			value := info.Types[call.Args[0]].Value
			if value == nil || value.Kind() != constant.String {
				return true
			}
			pattern := constant.StringVal(value)
			method, path, found := strings.Cut(pattern, " ")
			if !found {
				path = method
				method = "*"
			}
			path = strings.TrimLeft(path, " \t")
			result = append(result, Route{method, path, fmt.Sprintf("%s:%d", filename, fset.Position(call.Pos()).Line)})
			return true
		})
	}
	return result, nil
}

// Print writes a sorted route list as JSON or a table.
func Print(out io.Writer, routes []Route, jsonOutput bool) error {
	if jsonOutput {
		if err := json.NewEncoder(out).Encode(routes); err != nil {
			return routeError("write routes JSON", err)
		}
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "METHOD\tPATH\tSOURCE"); err != nil {
		return routeError("write routes table", err)
	}
	for _, route := range routes {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", route.Method, route.Path, route.Source); err != nil {
			return routeError("write routes table", err)
		}
	}
	if err := w.Flush(); err != nil {
		return routeError("write routes table", err)
	}
	return nil
}

func routeError(action string, err error) error {
	return fmt.Errorf("%s: %w\nTry: check the application files and permissions, then run spinup routes", action, err)
}
