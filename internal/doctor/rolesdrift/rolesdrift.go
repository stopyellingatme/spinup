package rolesdrift

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
)

const mainPath = "api/cmd/api/main.go"

func init() {
	doctor.Register(doctor.Check{ID: "roles-drift", Title: "Role configuration drift", Scope: doctor.Local, Quick: true, Run: runCheck})
}

func runCheck(_ context.Context, c *doctor.Context) doctor.Result {
	if c.Root == "" {
		return skip("outside a spinup project")
	}
	lock, err := manifest.Load(filepath.Join(c.Root, project.LockFile))
	if errors.Is(err, os.ErrNotExist) {
		return skip("outside a spinup project: spinup.lock is missing")
	}
	if err != nil {
		return warn("cannot read spinup.lock", "correct spinup.lock")
	}
	if !slices.Contains(lock.Packs, "auth") {
		return skip("no auth module")
	}
	config, err := project.Load(c.Root)
	if err != nil {
		return warn("cannot read spinup.toml", "correct spinup.toml")
	}
	roles := config.AuthRoles()
	if err := roles.Validate(); err != nil {
		return warn("spinup.toml has invalid roles", "correct [auth] in spinup.toml and run spinup g roles")
	}
	want := project.RenderRolesFile(roles)
	path := filepath.Join(c.Root, filepath.FromSlash(project.RolesFilePath))
	got, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return warn("cannot read "+project.RolesFilePath, "check the file permissions")
	}
	if errors.Is(err, os.ErrNotExist) || !bytes.Equal(got, want) {
		return warn(project.RolesFilePath+" does not match [auth] in spinup.toml.", "spinup g roles")
	}
	main, err := os.ReadFile(filepath.Join(c.Root, filepath.FromSlash(mainPath)))
	if err != nil {
		return warn("cannot read "+mainPath, "check the file permissions")
	}
	if !hasMainCalls(main) {
		return warn(mainPath+" does not pass the role set to the auth module.", "add auth.WithRoles(roles.Set), auth.WithRegistration(...) and app.WithCommand(\"user\", auth.UserCommand(a)) to main.go")
	}
	return doctor.Result{Status: doctor.OK, Message: "generated roles match [auth] in spinup.toml"}
}

func hasMainCalls(source []byte) bool {
	file, err := parser.ParseFile(token.NewFileSet(), mainPath, source, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	var withRoles, withRegistration, userCommand bool
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch callName(call) {
		case "WithRoles":
			withRoles = true
		case "WithRegistration":
			withRegistration = true
		case "WithCommand":
			userCommand = userCommand || isUserCommandRegistration(call)
		}
		return true
	})
	return withRoles && withRegistration && userCommand
}

func callName(call *ast.CallExpr) string {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	return selector.Sel.Name
}

func isUserCommandRegistration(call *ast.CallExpr) bool {
	if len(call.Args) != 2 || stringLiteral(call.Args[0]) != "user" {
		return false
	}
	command, ok := call.Args[1].(*ast.CallExpr)
	return ok && callName(command) == "UserCommand"
}

func stringLiteral(expr ast.Expr) string {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return ""
	}
	return literal.Value[1 : len(literal.Value)-1]
}

func skip(reason string) doctor.Result {
	return doctor.Result{Status: doctor.Skip, Message: reason}
}

func warn(message, try string) doctor.Result {
	return doctor.Result{Status: doctor.Warn, Message: message, Try: try}
}
