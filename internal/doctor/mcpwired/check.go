package mcpwired

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

const (
	mainPath    = "api/cmd/api/main.go"
	appPath     = "github.com/stopyellingatme/spinup/kit/app"
	apikeysPath = "github.com/stopyellingatme/spinup/kit/x/apikeys"
	appMCPPath  = "/api/internal/mcp"
	docsHint    = " (see docs/modules/mcp.md)"
)

func init() {
	doctor.Register(doctor.Check{ID: "mcp-wired", Title: "MCP server wiring", Scope: doctor.Local, Quick: true, Run: runCheck})
}

func warn(message, try string) doctor.Result {
	return doctor.Result{Status: doctor.Warn, Message: message, Try: try}
}

func runCheck(_ context.Context, c *doctor.Context) doctor.Result {
	if c.Root == "" {
		return doctor.Result{Status: doctor.Skip, Message: "outside a spinup project"}
	}
	lock, err := manifest.Load(filepath.Join(c.Root, "spinup.lock"))
	if errors.Is(err, fs.ErrNotExist) {
		return doctor.Result{Status: doctor.Skip, Message: "spinup.lock is missing"}
	}
	if err != nil {
		return warn("cannot read spinup.lock", "correct spinup.lock")
	}
	if !slices.Contains(lock.Packs, "mcp") {
		return doctor.Result{Status: doctor.Skip, Message: "no mcp module"}
	}
	source, err := os.ReadFile(filepath.Join(c.Root, mainPath))
	if err != nil {
		return warn("cannot read "+mainPath, "restore "+mainPath+" and add the MCP wiring"+docsHint)
	}
	return checkWiring(source)
}

// imports maps an import path to the name that the file uses for it.
// The app MCP package is found by its path suffix, because the module path
// belongs to the app.
type imports struct {
	app, apikeys, appMCP string
}

func importNames(file *ast.File) imports {
	names := imports{app: "app", apikeys: "apikeys"}
	for _, imp := range file.Imports {
		value, _ := strconv.Unquote(imp.Path.Value)
		name := ""
		if imp.Name != nil {
			name = imp.Name.Name
		}
		pick := func(fallback string) string {
			if name != "" {
				return name
			}
			return fallback
		}
		switch {
		case value == appPath:
			names.app = pick("app")
		case value == apikeysPath:
			names.apikeys = pick("apikeys")
		case strings.HasSuffix(value, appMCPPath):
			names.appMCP = pick("mcp")
		}
	}
	return names
}

func selector(expr ast.Expr, receiver, name string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == receiver && sel.Sel.Name == name
}

// findCall returns the first call of receiver.name in node.
func findCall(node ast.Node, receiver, name string) *ast.CallExpr {
	var found *ast.CallExpr
	ast.Inspect(node, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if c, ok := n.(*ast.CallExpr); ok && selector(c.Fun, receiver, name) {
			found = c
			return false
		}
		return true
	})
	return found
}

// binding returns the variable that receives the result of receiver.name(...).
func binding(file *ast.File, receiver, name string) string {
	var variable string
	ast.Inspect(file, func(n ast.Node) bool {
		if variable != "" {
			return false
		}
		assignment, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, expr := range assignment.Rhs {
			c, ok := expr.(*ast.CallExpr)
			if !ok || !selector(c.Fun, receiver, name) || i >= len(assignment.Lhs) {
				continue
			}
			if id, ok := assignment.Lhs[i].(*ast.Ident); ok && id.Name != "_" {
				variable = id.Name
				return false
			}
		}
		return true
	})
	return variable
}

func checkWiring(source []byte) doctor.Result {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", source, parser.SkipObjectResolution)
	if err != nil {
		return warn("cannot parse "+mainPath, "correct the Go syntax in "+mainPath)
	}
	names := importNames(file)
	missing := func(what, line string) doctor.Result {
		return warn(mainPath+" lacks "+what+".", "add "+line+docsHint)
	}
	if names.appMCP == "" {
		return missing("the import of the app MCP package", `appmcp "<module>/api/internal/mcp"`)
	}
	server := binding(file, names.appMCP, "New")
	if server == "" {
		return missing(names.appMCP+".New", "mcpServer, err := appmcp.New(appmcp.Deps{DB: pool, Version: version})")
	}
	appNew := findCall(file, names.app, "New")
	if appNew == nil || findCall(appNew, server, "Routes") == nil {
		return missing(server+".Routes inside app.New", "app.WithRoutes(mcpServer.Routes(web.PublicOrigin)),")
	}
	newStore := findCall(file, names.apikeys, "NewStore")
	rpcPath := false
	if newStore != nil {
		for _, arg := range newStore.Args {
			if c, ok := arg.(*ast.CallExpr); ok && selector(c.Fun, names.apikeys, "WithRPCPath") {
				rpcPath = true
			}
		}
	}
	if !rpcPath {
		return missing("apikeys.WithRPCPath inside apikeys.NewStore", "apikeys.WithRPCPath(kitmcp.Path) as an option of apikeys.NewStore")
	}
	return doctor.Result{Status: doctor.OK, Message: "mcp wiring is present"}
}
