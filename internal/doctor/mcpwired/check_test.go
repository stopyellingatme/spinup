package mcpwired

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

func writeLock(t *testing.T, root string, packs ...string) {
	t.Helper()
	lock := manifest.New()
	lock.Packs = packs
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
}

const wired = `package main

import (
	appmcp "example.com/demo/api/internal/mcp"
	"github.com/stopyellingatme/spinup/kit/app"
	"github.com/stopyellingatme/spinup/kit/x/apikeys"
	kitmcp "github.com/stopyellingatme/spinup/kit/x/mcp"
)

func run() error {
	keyStore, err := apikeys.NewStore(pool, appapikeys.Config, apikeys.WithRPCPath(kitmcp.Path))
	if err != nil {
		return err
	}
	mcpServer, err := appmcp.New(appmcp.Deps{DB: pool, Version: version})
	if err != nil {
		return err
	}
	a := app.New(
		app.WithRoutes(keyStore.Routes(authn)),
		app.WithRoutes(mcpServer.Routes(web.PublicOrigin)),
		app.WithCommand("mcp", mcpServer.Command()),
	)
	return a.Run(ctx)
}
`

func TestWired(t *testing.T) {
	if got := checkWiring([]byte(wired)); got.Status != doctor.OK {
		t.Fatalf("%+v", got)
	}
}

func TestMissingLines(t *testing.T) {
	for _, tc := range []struct {
		name, old, new, want string
	}{
		{"no import", `appmcp "example.com/demo/api/internal/mcp"`, `"fmt"`, `appmcp "<module>/api/internal/mcp"`},
		{"no New", "mcpServer, err := appmcp.New(appmcp.Deps{DB: pool, Version: version})", "var mcpServer *kitmcp.Server", "mcpServer, err := appmcp.New("},
		{"no Routes", "app.WithRoutes(mcpServer.Routes(web.PublicOrigin)),", "", "app.WithRoutes(mcpServer.Routes(web.PublicOrigin))"},
		{"Routes outside app.New", "app.WithRoutes(mcpServer.Routes(web.PublicOrigin)),", "", "app.WithRoutes(mcpServer.Routes(web.PublicOrigin))"},
		{"no WithRPCPath", ", apikeys.WithRPCPath(kitmcp.Path)", "", "apikeys.WithRPCPath(kitmcp.Path)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := strings.Replace(wired, tc.old, tc.new, 1)
			if tc.name == "Routes outside app.New" {
				source = strings.Replace(source, "return a.Run(ctx)", "_ = mcpServer.Routes(web.PublicOrigin)\n\treturn a.Run(ctx)", 1)
			}
			if source == wired {
				t.Fatal("the fixture did not change")
			}
			got := checkWiring([]byte(source))
			if got.Status != doctor.Warn || !strings.Contains(got.Try, tc.want) || !strings.Contains(got.Message, mainPath) {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestWithRPCPathOutsideNewStore(t *testing.T) {
	source := strings.Replace(wired, ", apikeys.WithRPCPath(kitmcp.Path)", "", 1)
	source = strings.Replace(source, "return a.Run(ctx)", "_ = apikeys.WithRPCPath(kitmcp.Path)\n\treturn a.Run(ctx)", 1)
	if got := checkWiring([]byte(source)); got.Status != doctor.Warn || !strings.Contains(got.Try, "WithRPCPath") {
		t.Fatalf("%+v", got)
	}
}

func TestAliasedImports(t *testing.T) {
	source := strings.NewReplacer(
		`appmcp "example.com/demo/api/internal/mcp"`, `tools "example.com/demo/api/internal/mcp"`,
		`"github.com/stopyellingatme/spinup/kit/app"`, `kitapp "github.com/stopyellingatme/spinup/kit/app"`,
		`"github.com/stopyellingatme/spinup/kit/x/apikeys"`, `keys "github.com/stopyellingatme/spinup/kit/x/apikeys"`,
		"appmcp.New(appmcp.Deps", "tools.New(tools.Deps",
		"app.New(", "kitapp.New(",
		"app.WithRoutes", "kitapp.WithRoutes",
		"app.WithCommand", "kitapp.WithCommand",
		"apikeys.NewStore", "keys.NewStore",
		"apikeys.WithRPCPath", "keys.WithRPCPath",
		"mcpServer", "srv",
	).Replace(wired)
	if got := checkWiring([]byte(source)); got.Status != doctor.OK {
		t.Fatalf("%+v", got)
	}
	// The default names do not match after the alias.
	broken := strings.Replace(source, "keys.WithRPCPath", "apikeys.WithRPCPath", 1)
	if got := checkWiring([]byte(broken)); got.Status != doctor.Warn {
		t.Fatalf("%+v", got)
	}
}

func TestUnaliasedAppPackage(t *testing.T) {
	source := strings.NewReplacer(
		`appmcp "example.com/demo/api/internal/mcp"`, `"example.com/demo/api/internal/mcp"`,
		"appmcp.", "mcp.",
	).Replace(wired)
	if got := checkWiring([]byte(source)); got.Status != doctor.OK {
		t.Fatalf("%+v", got)
	}
}

func TestParseError(t *testing.T) {
	if got := checkWiring([]byte("package main\nfunc {")); got.Status != doctor.Warn || got.Try == "" {
		t.Fatalf("%+v", got)
	}
}

func TestRunSkipsWithoutModule(t *testing.T) {
	root := t.TempDir()
	if got := runCheck(t.Context(), &doctor.Context{Root: root}); got.Status != doctor.Skip {
		t.Fatalf("%+v", got)
	}
	if got := runCheck(t.Context(), &doctor.Context{}); got.Status != doctor.Skip {
		t.Fatalf("%+v", got)
	}
	writeLock(t, root, "base", "api", "auth", "apikeys")
	if got := runCheck(t.Context(), &doctor.Context{Root: root}); got.Status != doctor.Skip || got.Message != "no mcp module" {
		t.Fatalf("%+v", got)
	}
}

func TestRunWithModule(t *testing.T) {
	root := t.TempDir()
	writeLock(t, root, "base", "api", "auth", "apikeys", "mcp")
	if got := runCheck(t.Context(), &doctor.Context{Root: root}); got.Status != doctor.Warn || !strings.Contains(got.Message, "cannot read") {
		t.Fatalf("%+v", got)
	}
	if err := os.MkdirAll(filepath.Join(root, "api/cmd/api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, mainPath), []byte(wired), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := runCheck(t.Context(), &doctor.Context{Root: root}); got.Status != doctor.OK {
		t.Fatalf("%+v", got)
	}
}
