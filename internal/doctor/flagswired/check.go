package flagswired

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

const mainPath = "api/cmd/api/main.go"

func init() {
	doctor.Register(doctor.Check{ID: "flags-wired", Title: "Feature flags wiring", Scope: doctor.Local, Quick: true, Run: runCheck})
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
	if !slices.Contains(lock.Packs, "flags") {
		return doctor.Result{Status: doctor.Skip, Message: "no flags module"}
	}
	source, err := os.ReadFile(filepath.Join(c.Root, mainPath))
	if err != nil {
		return warn("cannot read "+mainPath, "restore "+mainPath+" and add the flags wiring from docs/build/flags.md")
	}
	if result := checkWiring(source); result.Status != doctor.OK {
		return result
	}
	if slices.Contains(lock.Packs, "audit") {
		if result := checkTriggers(c.Root); result.Status != doctor.OK {
			return result
		}
	}
	return doctor.Result{Status: doctor.OK, Message: "flags wiring is present"}
}

func selector(expr ast.Expr, receiver, name string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == receiver && sel.Sel.Name == name
}

func call(expr ast.Expr, receiver, name string) (*ast.CallExpr, bool) {
	c, ok := expr.(*ast.CallExpr)
	return c, ok && selector(c.Fun, receiver, name)
}

func packageName(file *ast.File, path, fallback string) string {
	for _, imp := range file.Imports {
		value, _ := strconv.Unquote(imp.Path.Value)
		if value == path {
			if imp.Name != nil {
				return imp.Name.Name
			}
			return fallback
		}
	}
	return fallback
}

func binding(file *ast.File, pkg, method string) (string, *ast.CallExpr) {
	var name string
	var found *ast.CallExpr
	ast.Inspect(file, func(node ast.Node) bool {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, expr := range assignment.Rhs {
			c, ok := call(expr, pkg, method)
			if !ok || i >= len(assignment.Lhs) {
				continue
			}
			if id, ok := assignment.Lhs[i].(*ast.Ident); ok && id.Name != "_" {
				name, found = id.Name, c
				return false
			}
		}
		return true
	})
	return name, found
}

func hasCall(node ast.Node, receiver, method string) bool {
	found := false
	ast.Inspect(node, func(node ast.Node) bool {
		c, ok := node.(*ast.CallExpr)
		if ok && selector(c.Fun, receiver, method) {
			found = true
		}
		return true
	})
	return found
}

func checkWiring(source []byte) doctor.Result {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", source, parser.SkipObjectResolution)
	if err != nil {
		return warn("cannot parse "+mainPath, "correct the Go syntax in "+mainPath)
	}
	flags := packageName(file, "github.com/stopyellingatme/spinup/kit/x/flags", "flags")
	auth := packageName(file, "github.com/stopyellingatme/spinup/kit/auth", "auth")
	app := packageName(file, "github.com/stopyellingatme/spinup/kit/app", "app")
	missing := func(name, line string) doctor.Result { return warn(mainPath+" lacks "+name+" wiring.", line) }
	provider, newProvider := binding(file, flags, "New")
	if newProvider == nil {
		return missing("flags.New(", "flagsProvider, err := flags.New(flags.WithDeclared(appflags.Set))")
	}
	if !hasCall(newProvider, flags, "WithDeclared") {
		return missing("flags.WithDeclared(", "flagsProvider, err := flags.New(flags.WithDeclared(appflags.Set))")
	}
	store, newStore := binding(file, flags, "NewStore")
	if newStore == nil {
		return missing("flags.NewStore(", "flagStore := flags.NewStore(pool, flagsProvider, roles.Set)")
	}
	if !hasCall(file, store, "Start") {
		return missing(store+".Start(", "if err := flagStore.Start(ctx, flagsEnv.Refresh); err != nil { return err }")
	}
	authn, _ := binding(file, auth, "New")
	var appNew *ast.CallExpr
	ast.Inspect(file, func(node ast.Node) bool {
		if c, ok := node.(*ast.CallExpr); ok && selector(c.Fun, app, "New") {
			appNew = c
			return false
		}
		return true
	})
	var routes, command, shutdown bool
	providerAt, authAt := -1, -1
	if appNew != nil {
		for i, arg := range appNew.Args {
			option, ok := arg.(*ast.CallExpr)
			if !ok || len(option.Args) == 0 {
				continue
			}
			first := option.Args[0]
			switch {
			case selector(option.Fun, app, "WithMiddleware"):
				if _, ok := call(first, provider, "Middleware"); ok {
					providerAt = i
				}
				if _, ok := call(first, authn, "Middleware"); ok {
					authAt = i
				}
			case selector(option.Fun, app, "WithRoutes"):
				if _, ok := call(first, store, "Routes"); ok {
					routes = true
				}
			case selector(option.Fun, app, "WithCommand") && len(option.Args) == 2:
				key, ok := first.(*ast.BasicLit)
				if ok && key.Kind == token.STRING {
					value, _ := strconv.Unquote(key.Value)
					if _, ok := call(option.Args[1], store, "Command"); ok && value == "flags" {
						command = true
					}
				}
			case selector(option.Fun, app, "WithShutdown"):
				if selector(first, store, "Close") {
					shutdown = true
				}
			}
		}
	}
	if providerAt < 0 {
		return missing("app.WithMiddleware("+provider+".Middleware())", "app.WithMiddleware(flagsProvider.Middleware()),")
	}
	if authAt < 0 {
		if authn == "" {
			authn = "authn"
		}
		return missing("app.WithMiddleware("+authn+".Middleware())", "app.WithMiddleware(authn.Middleware()),")
	}
	if providerAt < authAt {
		return warn(mainPath+" registers the flags middleware before the auth middleware, so every subject is anonymous.", "move app.WithMiddleware(flagsProvider.Middleware()) after app.WithMiddleware(authn.Middleware())")
	}
	if !routes {
		return missing("app.WithRoutes("+store+".Routes(", "app.WithRoutes(flagStore.Routes(authn)),")
	}
	if !command {
		return missing(`app.WithCommand("flags", `, `app.WithCommand("flags", flagStore.Command()),`)
	}
	if !shutdown {
		return missing("app.WithShutdown("+store+".Close)", "app.WithShutdown(flagStore.Close),")
	}
	return doctor.Result{Status: doctor.OK, Message: "flags wiring is present"}
}

// These expressions inspect the Up sections of migrations, not arbitrary SQL.
var (
	sqlComments   = regexp.MustCompile(`(?s)/\*.*?\*/|--[^\n]*`)
	doBlock       = regexp.MustCompile(`(?is)\bDO\s+\$[^$]*\$.*?\$[^$]*\$\s*;`)
	functionGuard = regexp.MustCompile(`(?i)\bto_regprocedure\s*\(`)
	auditFunction = regexp.MustCompile(`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+(?:"?public"?\s*\.\s*)?"?audit_row_change"?\s*\(`)
	flagTrigger   = regexp.MustCompile(`(?is)\bCREATE\s+(?:OR\s+REPLACE\s+)?TRIGGER\s+[^;]+?\sON\s+(?:"?public"?\s*\.\s*)?"?feature_flags"?\s+[^;]*?EXECUTE\s+(?:FUNCTION|PROCEDURE)\s+(?:"?public"?\s*\.\s*)?"?audit_row_change"?\s*\(`)
)

type migration struct {
	space     int
	version   uint64
	name, sql string
}

func checkTriggers(root string) doctor.Result {
	var migrations []migration
	for space, dir := range []string{"api/db/kit", "api/db/migrations"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return warn("cannot read "+dir, "restore the migrations directory and check its permissions")
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
				continue
			}
			name := filepath.Join(dir, entry.Name())
			data, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				return warn("cannot read "+name, "check the migration file permissions")
			}
			up, _, _ := strings.Cut(string(data), "-- +goose Down")
			sql := sqlComments.ReplaceAllString(up, " ")
			number, _, _ := strings.Cut(entry.Name(), "_")
			version, _ := strconv.ParseUint(number, 10, 64)
			migrations = append(migrations, migration{space, version, name, sql})
		}
	}
	slices.SortFunc(migrations, func(a, b migration) int {
		if a.space != b.space {
			return a.space - b.space
		}
		if a.version < b.version {
			return -1
		}
		if a.version > b.version {
			return 1
		}
		return strings.Compare(a.name, b.name)
	})
	var functions, atRisk, attached bool
	for _, m := range migrations {
		functions = functions || auditFunction.MatchString(m.sql)
		if flagTrigger.MatchString(m.sql) && functionGuard.MatchString(m.sql) && !functions {
			atRisk = true
		}
		// A guarded DO block does not repair a previous conditional attach.
		unconditional := doBlock.ReplaceAllStringFunc(m.sql, func(block string) string {
			if functionGuard.MatchString(block) {
				return " "
			}
			return block
		})
		attached = attached || flagTrigger.MatchString(unconditional)
	}
	if atRisk && !attached {
		return warn("feature_flags can lack its audit triggers: the flags migration ran before the audit functions existed.", "run spinup db new audit_feature_flags, paste the four CREATE TRIGGER statements from docs/build/flags.md, then run spinup db migrate up")
	}
	return doctor.Result{Status: doctor.OK, Message: "flags wiring is present"}
}
