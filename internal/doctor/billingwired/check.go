package billingwired

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

	"go.yaml.in/yaml/v3"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

const (
	apiPath     = "api/cmd/api/main.go"
	workerPath  = "api/cmd/worker/main.go"
	composePath = "deploy/compose.yml"
	appPkg      = "github.com/stopyellingatme/spinup/kit/app"
	apikeysPkg  = "github.com/stopyellingatme/spinup/kit/x/apikeys"
	billingPkg  = "github.com/stopyellingatme/spinup/kit/x/billing"
	docsHint    = " (see docs/build/billing.md)"
	okMessage   = "billing wiring is present"
)

func init() {
	doctor.Register(doctor.Check{ID: "billing-wired", Title: "Billing wiring", Scope: doctor.Local, Quick: true, Run: runCheck})
}

func warn(message, try string) doctor.Result {
	return doctor.Result{Status: doctor.Warn, Message: message, Try: try}
}

func ok() doctor.Result { return doctor.Result{Status: doctor.OK, Message: okMessage} }

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
	if !slices.Contains(lock.Packs, "billing") {
		return doctor.Result{Status: doctor.Skip, Message: "no billing module"}
	}
	withKeys := slices.Contains(lock.Packs, "apikeys")
	for _, step := range []struct {
		path  string
		check func([]byte, bool) doctor.Result
	}{{apiPath, checkAPI}, {workerPath, checkWorker}} {
		source, err := os.ReadFile(filepath.Join(c.Root, step.path))
		if err != nil {
			return warn("cannot read "+step.path, "restore "+step.path+" and add the billing wiring"+docsHint)
		}
		if result := step.check(source, withKeys); result.Status != doctor.OK {
			return result
		}
	}
	if slices.Contains(lock.Packs, "deploy") {
		source, err := os.ReadFile(filepath.Join(c.Root, composePath))
		if err != nil {
			return warn("cannot read "+composePath, "restore "+composePath+" with spinup upgrade")
		}
		if result := checkCompose(source); result.Status != doctor.OK {
			return result
		}
	}
	if slices.Contains(lock.Packs, "audit") {
		if result := checkTriggers(c.Root); result.Status != doctor.OK {
			return result
		}
	}
	return ok()
}

// names maps the kit import paths to the names that a file uses for them.
type names struct{ app, apikeys, billing string }

func importNames(file *ast.File) names {
	// kitbilling is the alias of the generated files. It only shows in the fix text
	// when the file does not import the package at all.
	n := names{app: "app", apikeys: "apikeys", billing: "kitbilling"}
	for _, imp := range file.Imports {
		value, _ := strconv.Unquote(imp.Path.Value)
		pick := func(fallback string) string {
			if imp.Name != nil {
				return imp.Name.Name
			}
			return fallback
		}
		switch value {
		case appPkg:
			n.app = pick("app")
		case apikeysPkg:
			n.apikeys = pick("apikeys")
		case billingPkg:
			n.billing = pick("billing")
		}
	}
	return n
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

// binding returns the variable that receives receiver.name(...) and the call.
func binding(file *ast.File, receiver, name string) (string, *ast.CallExpr) {
	var variable string
	var found *ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if found != nil {
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
				variable, found = id.Name, c
				return false
			}
		}
		return true
	})
	return variable, found
}

// hasOption reports whether call has an argument receiver.option(...).
func hasOption(call *ast.CallExpr, receiver, option string) bool {
	for _, arg := range call.Args {
		if c, ok := arg.(*ast.CallExpr); ok && selector(c.Fun, receiver, option) {
			return true
		}
	}
	return false
}

func parse(path string, source []byte) (*ast.File, doctor.Result) {
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Base(path), source, parser.SkipObjectResolution)
	if err != nil {
		return nil, warn("cannot parse "+path, "correct the Go syntax in "+path)
	}
	return file, ok()
}

func lacks(path string) func(what, line string) doctor.Result {
	return func(what, line string) doctor.Result {
		return warn(path+" lacks "+what+".", "add "+line+docsHint)
	}
}

// newService checks the billing.New call of a main file and returns the service variable.
func newService(file *ast.File, n names, path string, withKeys bool, required ...string) (string, doctor.Result) {
	missing := lacks(path)
	service, newCall := binding(file, n.billing, "New")
	if newCall == nil {
		return "", missing(n.billing+".New", "billingSvc, err := kitbilling.New(pool, appbilling.Config, stripeClient, ...)")
	}
	for _, option := range required {
		if !hasOption(newCall, n.billing, option) {
			return "", missing(n.billing+"."+option+" inside "+n.billing+".New", optionLine(option)+" as an option of kitbilling.New")
		}
	}
	if withKeys && !hasOption(newCall, n.billing, "WithAPIKeys") {
		return "", missing(n.billing+".WithAPIKeys inside "+n.billing+".New", "kitbilling.WithAPIKeys(keyStore) as an option of kitbilling.New")
	}
	return service, ok()
}

func optionLine(option string) string {
	switch option {
	case "WithJobs":
		return "kitbilling.WithJobs(jobsClient)"
	case "WithWebhookSecrets":
		return "kitbilling.WithWebhookSecrets(billingEnv.WebhookSecret)"
	case "WithPublicOrigin":
		return "kitbilling.WithPublicOrigin(web.PublicOrigin)"
	}
	return "kitbilling." + option + "(...)"
}

// checkAPI checks the billing wiring of api/cmd/api/main.go.
func checkAPI(source []byte, withKeys bool) doctor.Result {
	file, result := parse(apiPath, source)
	if file == nil {
		return result
	}
	n := importNames(file)
	missing := lacks(apiPath)
	service, result := newService(file, n, apiPath, withKeys, "WithJobs", "WithWebhookSecrets", "WithPublicOrigin")
	if result.Status != doctor.OK {
		return result
	}
	if withKeys {
		newStore := findCall(file, n.apikeys, "NewStore")
		sessionOnly := false
		if newStore != nil {
			for _, arg := range newStore.Args {
				if c, ok := arg.(*ast.CallExpr); ok && selector(c.Fun, n.apikeys, "WithSessionOnly") && len(c.Args) > 0 && selector(c.Args[0], n.billing, "SessionPrefix") {
					sessionOnly = true
				}
			}
		}
		if !sessionOnly {
			return missing("apikeys.WithSessionOnly inside apikeys.NewStore", "apikeys.WithSessionOnly(kitbilling.SessionPrefix) as an option of apikeys.NewStore")
		}
	}
	var routes, command bool
	if appNew := findCall(file, n.app, "New"); appNew != nil {
		for _, arg := range appNew.Args {
			option, ok := arg.(*ast.CallExpr)
			if !ok || len(option.Args) == 0 {
				continue
			}
			switch {
			case selector(option.Fun, n.app, "WithRoutes"):
				if c, ok := option.Args[0].(*ast.CallExpr); ok && selector(c.Fun, service, "Routes") {
					routes = true
				}
			case selector(option.Fun, n.app, "WithCommand") && len(option.Args) == 2:
				key, isLit := option.Args[0].(*ast.BasicLit)
				c, isCall := option.Args[1].(*ast.CallExpr)
				if isLit && key.Kind == token.STRING && isCall && selector(c.Fun, service, "Command") {
					value, _ := strconv.Unquote(key.Value)
					command = command || value == "billing"
				}
			}
		}
	}
	if !routes {
		return missing(service+".Routes inside app.New", "app.WithRoutes(billingSvc.Routes(authn)),")
	}
	if !command {
		return missing(`the "billing" command inside app.New`, `app.WithCommand("billing", billingSvc.Command()),`)
	}
	return ok()
}

// checkWorker checks the billing wiring of api/cmd/worker/main.go.
func checkWorker(source []byte, withKeys bool) doctor.Result {
	file, result := parse(workerPath, source)
	if file == nil {
		return result
	}
	n := importNames(file)
	service, result := newService(file, n, workerPath, withKeys)
	if result.Status != doctor.OK {
		return result
	}
	if findCall(file, service, "Register") == nil {
		return lacks(workerPath)(service+".Register", "billingSvc.Register(w) before newWorker returns w")
	}
	return ok()
}

// secrets are the environment entries that the api and worker services need.
var secrets = []string{"STRIPE_SECRET_KEY_FILE", "STRIPE_WEBHOOK_SECRET_FILE"}

// checkCompose checks that the api and worker services read the Stripe secrets.
func checkCompose(source []byte) doctor.Result {
	var compose struct {
		Services map[string]struct {
			Environment any `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(source, &compose); err != nil {
		return warn("cannot parse "+composePath, "correct the YAML syntax in "+composePath)
	}
	for _, service := range []string{"api", "worker"} {
		env := environment(compose.Services[service].Environment)
		for _, name := range secrets {
			if _, ok := env[name]; !ok {
				secret := strings.TrimSuffix(name, "_FILE")
				return warn(composePath+" lacks "+name+" in the "+service+" service.",
					"add "+name+": /run/secrets/"+secret+" to services."+service+".environment, or run spinup upgrade to restore the managed file")
			}
		}
	}
	return ok()
}

// environment reads a Compose environment in the map form or the list form.
func environment(value any) map[string]bool {
	out := map[string]bool{}
	switch env := value.(type) {
	case map[string]any:
		for key := range env {
			out[key] = true
		}
	case []any:
		for _, item := range env {
			if entry, ok := item.(string); ok {
				key, _, _ := strings.Cut(entry, "=")
				out[strings.TrimSpace(key)] = true
			}
		}
	}
	return out
}

// These expressions inspect the Up sections of migrations, not arbitrary SQL.
var (
	sqlComments   = regexp.MustCompile(`(?s)/\*.*?\*/|--[^\n]*`)
	doBlock       = regexp.MustCompile(`(?is)\bDO\s+\$[^$]*\$.*?\$[^$]*\$\s*;`)
	functionGuard = regexp.MustCompile(`(?i)\bto_regprocedure\s*\(`)
	auditFunction = regexp.MustCompile(`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+(?:"?public"?\s*\.\s*)?"?audit_row_change"?\s*\(`)
)

// trigger is one audit trigger of a billing table.
type trigger struct {
	pattern   *regexp.Regexp
	statement string
}

func rowTrigger(table, record string) trigger {
	return trigger{
		pattern: regexp.MustCompile(`(?is)\bCREATE\s+(?:OR\s+REPLACE\s+)?TRIGGER\s+` + table + `_audit\b[^;]+\bON\s+(?:public\.)?` + table +
			`\b[^;]+audit_row_change\s*\(\s*'user_id'\s*,\s*'` + record + `'\s*\)`),
		statement: "CREATE TRIGGER " + table + "_audit AFTER INSERT OR UPDATE OR DELETE ON public." + table +
			" FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', '" + record + "');",
	}
}

func truncateTrigger(table string) trigger {
	return trigger{
		pattern: regexp.MustCompile(`(?is)\bCREATE\s+(?:OR\s+REPLACE\s+)?TRIGGER\s+` + table + `_audit_truncate\b[^;]+\bON\s+(?:public\.)?` + table +
			`\b[^;]+audit_truncate\s*\(`),
		statement: "CREATE TRIGGER " + table + "_audit_truncate AFTER TRUNCATE ON public." + table +
			" FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();",
	}
}

// triggers are the audit triggers of ADR 0032 section 7.1, in the order of the fix text.
var triggers = []trigger{
	rowTrigger("billing_customers", "user_id"),
	truncateTrigger("billing_customers"),
	rowTrigger("billing_subscriptions", "id"),
	truncateTrigger("billing_subscriptions"),
	rowTrigger("billing_entitlements", "user_id"),
	truncateTrigger("billing_entitlements"),
}

type migration struct {
	space     int
	version   uint64
	name, sql string
}

// checkTriggers reads the migrations in run order. A trigger inside a DO block that
// checks for the audit functions counts only when an earlier migration created them.
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
			number, _, _ := strings.Cut(entry.Name(), "_")
			version, _ := strconv.ParseUint(number, 10, 64)
			migrations = append(migrations, migration{space, version, name, sqlComments.ReplaceAllString(up, " ")})
		}
	}
	slices.SortFunc(migrations, func(a, b migration) int {
		if a.space != b.space {
			return a.space - b.space
		}
		if a.version != b.version {
			if a.version < b.version {
				return -1
			}
			return 1
		}
		return strings.Compare(a.name, b.name)
	})
	functions := false
	attached := make([]bool, len(triggers))
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
		for i, t := range triggers {
			attached[i] = attached[i] || t.pattern.MatchString(sql)
		}
	}
	var lines []string
	for i, present := range attached {
		if !present {
			lines = append(lines, triggers[i].statement)
		}
	}
	if len(lines) > 0 {
		return warn("billing audit triggers are missing or may not have been attached.",
			"run spinup db new audit_billing and add:\n"+strings.Join(lines, "\n"))
	}
	return ok()
}
