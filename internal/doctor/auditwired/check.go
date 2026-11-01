package auditwired

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
	"strings"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func init() {
	doctor.Register(doctor.Check{ID: "audit-wired", Title: "Audit trail wiring", Scope: doctor.Local, Quick: true, Run: runCheck})
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
	if !slices.Contains(lock.Packs, "audit") {
		return doctor.Result{Status: doctor.Skip, Message: "no audit module"}
	}
	main, err := os.ReadFile(filepath.Join(c.Root, "api/cmd/api/main.go"))
	if err != nil {
		return warn("cannot read api/cmd/api/main.go", "restore api/cmd/api/main.go and add the audit wiring from docs/audit.md")
	}
	if !hasWiring(main) {
		return warn("api/cmd/api/main.go lacks audit.Command( or authsink. wiring", `add auth.WithEvents(authsink.New(pool)), auth.WithActorContext(authsink.ActorContext), and app.WithCommand("audit", audit.Command(pool)) with the audit and authsink imports`)
	}
	dir := filepath.Join(c.Root, "api/db/migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return warn("cannot read api/db/migrations", "restore the migrations directory and check its permissions")
	}
	var identities, trigger bool
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return warn("cannot read api/db/migrations/"+entry.Name(), "check the migration file permissions")
		}
		// Only the Up section describes the installed schema. Ignore SQL comments.
		up, _, _ := strings.Cut(string(data), "-- +goose Down")
		sql := sqlComments.ReplaceAllString(up, " ")
		identities = identities || identityTable.MatchString(sql)
		trigger = trigger || identityTrigger.MatchString(sql)
	}
	if identities && !trigger {
		return warn("user_identities exists in the migrations without an audit trigger", "spinup g audit user_identities, then run spinup db migrate up")
	}
	return doctor.Result{Status: doctor.OK, Message: "audit wiring and identity triggers are present"}
}

// These expressions inspect migrations only. They do not claim to parse arbitrary SQL.
var (
	sqlComments     = regexp.MustCompile(`(?s)/\*.*?\*/|--[^\n]*`)
	identityTable   = regexp.MustCompile(`(?i)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:"?public"?\s*\.\s*)?"?user_identities"?\s*\(`)
	identityTrigger = regexp.MustCompile(`(?is)\bCREATE\s+(?:OR\s+REPLACE\s+)?TRIGGER\s+[^;]+?\sON\s+(?:"?public"?\s*\.\s*)?"?user_identities"?\s+[^;]*?EXECUTE\s+(?:FUNCTION|PROCEDURE)\s+(?:"?public"?\s*\.\s*)?"?audit_row_change"?\s*\(`)
)

func hasWiring(source []byte) bool {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", source, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	var command, sink bool
	ast.Inspect(file, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok {
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "audit" && selector.Sel.Name == "Command" {
					command = true
				}
			}
		}
		if selector, ok := node.(*ast.SelectorExpr); ok {
			if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "authsink" {
				sink = true
			}
		}
		return true
	})
	return command && sink
}

func warn(message, try string) doctor.Result {
	return doctor.Result{Status: doctor.Warn, Message: message, Try: try}
}
