package resource

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"

	"github.com/stopyellingatme/spinup/internal/project"
)

// getHistoryRoles reads the Get guard from the Go syntax tree. Comments and
// guards on other operations cannot grant access to history.
func getHistoryRoles(source []byte, spec Spec, config *project.Config) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "resource.go", source, 0)
	if err != nil {
		return nil, fmt.Errorf("cannot read the Get guard for %s: %w\nTry: fix the resource handler", spec.Name.Table(), err)
	}
	var get *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv != nil && fn.Name.Name == "Get"+spec.Name.Pascal() {
			get = fn
			break
		}
	}
	if get == nil || get.Body == nil {
		return nil, fmt.Errorf("cannot read the Get guard for %s\nTry: review the handler and add the history route by hand", spec.Name.Table())
	}
	// Only the generated negative guard can define the history roles.
	guarded := map[*ast.CallExpr]bool{}
	for _, stmt := range get.Body.List {
		branch, ok := stmt.(*ast.IfStmt)
		if !ok {
			continue
		}
		negative, ok := branch.Cond.(*ast.UnaryExpr)
		if !ok || negative.Op != token.NOT {
			continue
		}
		call, ok := negative.X.(*ast.CallExpr)
		if ok {
			guarded[call] = true
		}
	}
	var roles []string
	calls := 0
	ast.Inspect(get.Body, func(node ast.Node) bool {
		if err != nil {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HasRole" {
			return true
		}
		if !guarded[call] {
			err = fmt.Errorf("cannot interpret the custom Get guard for %s\nTry: add the history route by hand with the same guard", spec.Name.Table())
			return false
		}
		calls++
		if calls > 1 || len(call.Args) == 0 {
			err = fmt.Errorf("cannot read multiple or empty role guards for %s\nTry: add the history route by hand with the same guard", spec.Name.Table())
			return false
		}
		for _, arg := range call.Args {
			c, ok := arg.(*ast.SelectorExpr)
			found := false
			if ok {
				pkg, ok := c.X.(*ast.Ident)
				if ok && pkg.Name == "roles" {
					for _, role := range config.AuthRoles().Names {
						if c.Sel.Name == project.RoleConst(role) {
							roles = append(roles, role)
							found = true
							break
						}
					}
				}
			}
			if !found {
				err = fmt.Errorf("cannot read the role guard for %s\nTry: use declared role constants in the Get guard", spec.Name.Table())
				return false
			}
		}
		return true
	})
	return roles, err
}
