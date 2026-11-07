package apikeyswired

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
	doctor.Register(doctor.Check{ID: "apikeys-wired", Title: "API keys wiring", Scope: doctor.Local, Quick: true, Run: runCheck})
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
	if !slices.Contains(lock.Packs, "apikeys") {
		return doctor.Result{Status: doctor.Skip, Message: "no apikeys module"}
	}
	source, err := os.ReadFile(filepath.Join(c.Root, mainPath))
	if err != nil {
		return warn("cannot read "+mainPath, "restore "+mainPath+" and add the apikeys wiring from docs/build/api-keys.md")
	}
	if result := checkWiring(source); result.Status != doctor.OK {
		return result
	}
	if slices.Contains(lock.Packs, "audit") {
		if result := checkTriggers(c.Root); result.Status != doctor.OK {
			return result
		}
	}
	return doctor.Result{Status: doctor.OK, Message: "apikeys wiring is present"}
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
	keys := packageName(file, "github.com/stopyellingatme/spinup/kit/x/apikeys", "apikeys")
	auth := packageName(file, "github.com/stopyellingatme/spinup/kit/auth", "auth")
	app := packageName(file, "github.com/stopyellingatme/spinup/kit/app", "app")
	missing := func(name, line string) doctor.Result { return warn(mainPath+" lacks "+name+" wiring.", line) }
	store, newStore := binding(file, keys, "NewStore")
	if newStore == nil {
		return missing("apikeys.NewStore", "keyStore, err := apikeys.NewStore(pool, appapikeys.Config)")
	}
	if !hasCall(file, store, "Start") {
		return missing(store+".Start", "if err := keyStore.Start(ctx, apikeysEnv.Flush); err != nil { return err }")
	}
	authn, authNew := binding(file, auth, "New")
	bearer := false
	if authNew != nil {
		for _, arg := range authNew.Args {
			if c, ok := call(arg, auth, "WithBearer"); ok && len(c.Args) == 1 {
				if id, ok := c.Args[0].(*ast.Ident); ok && id.Name == store {
					bearer = true
				}
			}
		}
	}
	if !bearer {
		return missing("auth.WithBearer", "auth.WithBearer(keyStore),")
	}
	var appNew *ast.CallExpr
	ast.Inspect(file, func(node ast.Node) bool {
		if c, ok := node.(*ast.CallExpr); ok && selector(c.Fun, app, "New") {
			appNew = c
			return false
		}
		return true
	})
	var routes, command, shutdown bool
	keyAt, authAt, flagsAt := -1, -1, -1
	flagPkg := packageName(file, "github.com/stopyellingatme/spinup/kit/x/flags", "flags")
	provider, _ := binding(file, flagPkg, "New")
	if appNew != nil {
		for i, arg := range appNew.Args {
			option, ok := arg.(*ast.CallExpr)
			if !ok || len(option.Args) == 0 {
				continue
			}
			first := option.Args[0]
			switch {
			case selector(option.Fun, app, "WithMiddleware"):
				if _, ok := call(first, store, "Middleware"); ok {
					keyAt = i
				}
				if _, ok := call(first, authn, "Middleware"); ok {
					authAt = i
				}
				if provider != "" {
					if _, ok := call(first, provider, "Middleware"); ok {
						flagsAt = i
					}
				}
			case selector(option.Fun, app, "WithRoutes"):
				_, found := call(first, store, "Routes")
				routes = routes || found
			case selector(option.Fun, app, "WithCommand") && len(option.Args) == 2:
				if key, ok := first.(*ast.BasicLit); ok && key.Kind == token.STRING {
					value, _ := strconv.Unquote(key.Value)
					_, found := call(option.Args[1], store, "Command")
					command = command || (found && value == "apikeys")
				}
			case selector(option.Fun, app, "WithShutdown"):
				shutdown = shutdown || selector(first, store, "Close")
			}
		}
	}
	if keyAt < 0 {
		return missing("API keys middleware", "app.WithMiddleware(keyStore.Middleware()),")
	}
	if authAt < 0 {
		return missing("auth middleware", "app.WithMiddleware(authn.Middleware()),")
	}
	if keyAt < authAt || (flagsAt >= 0 && keyAt < flagsAt) {
		return warn(mainPath+" registers the API keys middleware too early.", "move app.WithMiddleware(keyStore.Middleware()) after the auth and flags middleware")
	}
	if !routes {
		return missing("API keys routes", "app.WithRoutes(keyStore.Routes(authn)),")
	}
	if !command {
		return missing("API keys command", `app.WithCommand("apikeys", keyStore.Command()),`)
	}
	if !shutdown {
		return missing("API keys shutdown", "app.WithShutdown(keyStore.Close),")
	}
	return doctor.Result{Status: doctor.OK, Message: "apikeys wiring is present"}
}

// These expressions inspect the Up sections of migrations, not arbitrary SQL.
var (
	sqlComments   = regexp.MustCompile(`(?s)/\*.*?\*/|--[^\n]*`)
	doBlock       = regexp.MustCompile(`(?is)\bDO\s+\$[^$]*\$.*?\$[^$]*\$\s*;`)
	functionGuard = regexp.MustCompile(`(?i)\bto_regprocedure\s*\(`)
	auditFunction = regexp.MustCompile(`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+(?:"?public"?\s*\.\s*)?"?audit_row_change"?\s*\(`)
	keyRow        = regexp.MustCompile(`(?is)\bCREATE\s+(?:OR\s+REPLACE\s+)?TRIGGER\s+api_keys_audit\b[^;]+\bON\s+(?:public\.)?api_keys\b[^;]+audit_row_change\s*\(\s*'user_id'\s*,\s*'id'\s*,\s*'token_hash'\s*\)`)
	keyTruncate   = regexp.MustCompile(`(?is)\bCREATE\s+(?:OR\s+REPLACE\s+)?TRIGGER\s+api_keys_audit_truncate\b[^;]+\bON\s+(?:public\.)?api_keys\b[^;]+audit_truncate\s*\(`)
	planRow       = regexp.MustCompile(`(?is)\bCREATE\s+(?:OR\s+REPLACE\s+)?TRIGGER\s+api_key_plans_audit\b[^;]+\bON\s+(?:public\.)?api_key_plans\b[^;]+audit_row_change\s*\(\s*'user_id'\s*,\s*'user_id'\s*\)`)
	planTruncate  = regexp.MustCompile(`(?is)\bCREATE\s+(?:OR\s+REPLACE\s+)?TRIGGER\s+api_key_plans_audit_truncate\b[^;]+\bON\s+(?:public\.)?api_key_plans\b[^;]+audit_truncate\s*\(`)
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
	var functions bool
	attached := [4]bool{}
	patterns := []*regexp.Regexp{keyRow, keyTruncate, planRow, planTruncate}
	for _, m := range migrations {
		functions = functions || auditFunction.MatchString(m.sql)
		sql := m.sql
		if !functions {
			sql = doBlock.ReplaceAllStringFunc(sql, func(block string) string {
				if functionGuard.MatchString(block) {
					return " "
				}
				return block
			})
		}
		for i, pattern := range patterns {
			attached[i] = attached[i] || pattern.MatchString(sql)
		}
	}
	for _, present := range attached {
		if !present {
			return warn("API keys audit triggers are missing or may not have been attached.", `add a migration with:
CREATE TRIGGER api_keys_audit AFTER INSERT OR UPDATE OR DELETE ON public.api_keys FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'id', 'token_hash');
CREATE TRIGGER api_keys_audit_truncate AFTER TRUNCATE ON public.api_keys FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();
CREATE TRIGGER api_key_plans_audit AFTER INSERT OR UPDATE OR DELETE ON public.api_key_plans FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'user_id');
CREATE TRIGGER api_key_plans_audit_truncate AFTER TRUNCATE ON public.api_key_plans FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();`)
		}
	}
	return doctor.Result{Status: doctor.OK, Message: "apikeys wiring is present"}
}
