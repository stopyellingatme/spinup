package livewired

import (
	"context"
	"errors"
	"fmt"
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
	genlive "github.com/stopyellingatme/spinup/internal/gen/live"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

const (
	mainPath      = "api/cmd/api/main.go"
	migrationPath = "api/db/kit/00011_live.sql"
	internalDir   = "api/internal"
	genDir        = "api/internal/gen"
	appPath       = "github.com/stopyellingatme/spinup/kit/app"
	kitLivePath   = "github.com/stopyellingatme/spinup/kit/x/live"
	appLiveSuffix = "/api/internal/live"
	docsHint      = " (see docs/build/live.md)"
)

// The wiring lines of api/cmd/api/main.go, in the form of the generated file.
const (
	lineImportApp   = `applive "<module>/api/internal/live"`
	lineImportKit   = `kitlive "github.com/stopyellingatme/spinup/kit/x/live"`
	lineNew         = "liveHub, err := kitlive.New(pool, kitlive.WithMaxStreams(liveEnv.MaxStreams), kitlive.WithMaxStreamsPerUser(liveEnv.MaxStreamsPerUser))"
	lineStart       = "if err := liveHub.Start(ctx); err != nil { return err }"
	lineRoutes      = "app.WithRoutes(applive.Routes(liveHub, applive.Deps{DB: pool})),"
	lineExempt      = "app.WithRequestTimeoutExempt(kitlive.IsStream),"
	lineDrain       = "app.WithDrain(liveHub.Drain),"
	lineShutdown    = "app.WithShutdown(liveHub.Close),"
	okMessage       = "live wiring is present"
	publishTryLine  = "add live.Publish to the handlers of the resource (see docs/build/live.md), or remove the feed"
	migrationTry    = "run spinup upgrade, or spinup module add live, to restore " + migrationPath
	generatedTryFmt = "run spinup g live <resource> --force, or add %s(mux, h, d) to " + genlive.GeneratedPath
)

func init() {
	doctor.Register(doctor.Check{ID: "live-wired", Title: "Live data wiring", Scope: doctor.Local, Quick: true, Run: runCheck})
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
	if !slices.Contains(lock.Packs, "live") {
		return doctor.Result{Status: doctor.Skip, Message: "no live module"}
	}
	return checkProject(c.Root)
}

// checkProject runs the checks of the wiring, the migration, and the feeds.
func checkProject(root string) doctor.Result {
	source, err := os.ReadFile(filepath.Join(root, mainPath))
	if err != nil {
		return warn("cannot read "+mainPath+".", "restore "+mainPath+" and add the live wiring"+docsHint)
	}
	if result := checkWiring(source); result.Status != doctor.OK {
		return result
	}
	if _, err := os.Stat(filepath.Join(root, migrationPath)); err != nil {
		return warn(migrationPath+" is missing. The tables live_feeds and live_outbox have no migration.", migrationTry)
	}
	return checkFeeds(root)
}

// imports holds the names that main.go uses for the packages of the wiring.
// The app live package is found by its path suffix, because the module path
// belongs to the app.
type imports struct {
	app, kitLive, appLive string
}

func importNames(file *ast.File) imports {
	names := imports{app: "app"}
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
		case value == kitLivePath:
			names.kitLive = pick("live")
		case strings.HasSuffix(value, appLiveSuffix):
			names.appLive = pick("live")
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

// calls returns every call of receiver.name in node, in source order.
func calls(node ast.Node, receiver, name string) []*ast.CallExpr {
	var found []*ast.CallExpr
	ast.Inspect(node, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok && selector(c.Fun, receiver, name) {
			found = append(found, c)
		}
		return true
	})
	return found
}

// callWithArg reports whether node calls receiver.name with the argument
// argReceiver.argName, such as app.WithDrain(liveHub.Drain).
func callWithArg(node ast.Node, receiver, name, argReceiver, argName string) bool {
	for _, c := range calls(node, receiver, name) {
		for _, arg := range c.Args {
			if selector(arg, argReceiver, argName) {
				return true
			}
		}
	}
	return false
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

// checkWiring checks the lines of main.go (checks 1 to 3 of the wiring). It
// reports the first missing line.
func checkWiring(source []byte) doctor.Result {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", source, parser.SkipObjectResolution)
	if err != nil {
		return warn("cannot parse "+mainPath+".", "correct the Go syntax in "+mainPath)
	}
	names := importNames(file)
	missing := func(what, line string) doctor.Result {
		return warn(mainPath+" lacks "+what+".", "add "+line+docsHint)
	}
	if names.appLive == "" {
		return missing("the import of the app live package", lineImportApp)
	}
	if names.kitLive == "" {
		return missing("the import of the kit live package", lineImportKit)
	}
	hub := binding(file, names.kitLive, "New")
	if hub == "" {
		return missing("a variable that holds the result of "+names.kitLive+".New", lineNew)
	}
	if len(calls(file, hub, "Start")) == 0 {
		return missing(hub+".Start", lineStart)
	}
	var appNew *ast.CallExpr
	if found := calls(file, names.app, "New"); len(found) > 0 {
		appNew = found[0]
	}
	inside := " inside " + names.app + ".New"
	if appNew == nil || len(calls(appNew, names.appLive, "Routes")) == 0 {
		return missing(names.appLive+".Routes"+inside, lineRoutes)
	}
	if !callWithArg(appNew, names.app, "WithRequestTimeoutExempt", names.kitLive, "IsStream") {
		return missing(names.app+".WithRequestTimeoutExempt("+names.kitLive+".IsStream)"+inside, lineExempt)
	}
	if !callWithArg(appNew, names.app, "WithDrain", hub, "Drain") {
		return missing(names.app+".WithDrain("+hub+".Drain)"+inside, lineDrain)
	}
	if !callWithArg(appNew, names.app, "WithShutdown", hub, "Close") {
		return missing(names.app+".WithShutdown("+hub+".Close)"+inside, lineShutdown)
	}
	return doctor.Result{Status: doctor.OK, Message: okMessage}
}

// checkFeeds checks each feed marker (checks 5 and 6): feeds_gen.go registers
// the feed, and a handler publishes to it.
func checkFeeds(root string) doctor.Result {
	feeds, err := genlive.ScanFeeds(root, nil)
	if err != nil {
		return warn("cannot read the live feed markers.", firstLine(err))
	}
	if len(feeds) == 0 {
		return doctor.Result{Status: doctor.OK, Message: okMessage}
	}
	registered, err := generatedCalls(root)
	if err != nil {
		return warn("cannot parse "+genlive.GeneratedPath+".", "run spinup g live <resource> --force to write the file again")
	}
	published, err := publishedFeeds(root)
	if err != nil {
		return warn("cannot read the Go files under "+internalDir+".", firstLine(err))
	}
	for _, feed := range feeds {
		if !registered[feed.Func] {
			return warn(genlive.GeneratedPath+" does not call "+feed.Func+", so the feed "+feed.Name+" has no routes.",
				fmt.Sprintf(generatedTryFmt, feed.Func))
		}
		if !published[feed.Name] {
			return warn("the feed "+feed.Name+" has no Publish call. A write that does not publish does not reach the clients.", publishTryLine)
		}
	}
	return doctor.Result{Status: doctor.OK, Message: okMessage}
}

// firstLine returns the Try text of an error with a "Try:" line, or a default.
func firstLine(err error) string {
	if _, try, ok := strings.Cut(err.Error(), "\nTry: "); ok {
		return try
	}
	return "check the files under " + genlive.Dir
}

// generatedCalls returns the names of the functions that feeds_gen.go calls.
// A missing file calls nothing.
func generatedCalls(root string) (map[string]bool, error) {
	out := map[string]bool{}
	source, err := os.ReadFile(filepath.Join(root, genlive.GeneratedPath))
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	file, err := parser.ParseFile(token.NewFileSet(), genlive.GeneratedPath, source, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	ast.Inspect(file, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok {
				out[id.Name] = true
			}
		}
		return true
	})
	return out, nil
}

// publishedFeeds returns the feed names that some non-test Go file under
// api/internal publishes to. The directories api/internal/live and
// api/internal/gen do not count. A file counts for a feed when it calls Publish
// of the kit live package and calls OwnedKey or SharedKey with the feed name as
// the first argument. The name is a string literal or a string constant of the file.
func publishedFeeds(root string) (map[string]bool, error) {
	out := map[string]bool{}
	base := filepath.Join(root, internalDir)
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && path == base {
				return filepath.SkipDir
			}
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == genlive.Dir || rel == genDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// A file that does not parse fails the build, and the build reports it.
		// The check skips the file.
		if file, parseErr := parser.ParseFile(token.NewFileSet(), rel, source, parser.SkipObjectResolution); parseErr == nil {
			for _, name := range fileFeeds(file) {
				out[name] = true
			}
		}
		return nil
	})
	return out, err
}

// fileFeeds returns the feed names that one file publishes to.
func fileFeeds(file *ast.File) []string {
	alias := ""
	for _, imp := range file.Imports {
		if value, _ := strconv.Unquote(imp.Path.Value); value == kitLivePath {
			alias = "live"
			if imp.Name != nil {
				alias = imp.Name.Name
			}
		}
	}
	if alias == "" || alias == "_" || len(calls(file, alias, "Publish")) == 0 {
		return nil
	}
	constants := stringConstants(file)
	var names []string
	for _, fn := range []string{"OwnedKey", "SharedKey"} {
		for _, c := range calls(file, alias, fn) {
			if len(c.Args) == 0 {
				continue
			}
			if name, ok := stringValue(c.Args[0], constants); ok {
				names = append(names, name)
			}
		}
	}
	return names
}

// stringConstants returns the string constants of a file, at any level.
func stringConstants(file *ast.File) map[string]string {
	out := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		decl, ok := n.(*ast.GenDecl)
		if !ok || decl.Tok != token.CONST {
			return true
		}
		for _, spec := range decl.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, id := range value.Names {
				if i < len(value.Values) {
					if s, ok := stringValue(value.Values[i], nil); ok {
						out[id.Name] = s
					}
				}
			}
		}
		return true
	})
	return out
}

// stringValue returns the value of a string literal, or of an identifier
// that names a string constant.
func stringValue(expr ast.Expr, constants map[string]string) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.Ident:
		s, ok := constants[e.Name]
		return s, ok
	}
	return "", false
}
