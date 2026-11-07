package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	appapikeys "example.com/todo/api/internal/apikeys"
	appmcp "example.com/todo/api/internal/mcp"
	"github.com/stopyellingatme/spinup/kit/x/apikeys"
	kitmcp "github.com/stopyellingatme/spinup/kit/x/mcp"

	kitmigrations "example.com/todo/api/db/kit"
	"example.com/todo/api/db/migrations"
	"example.com/todo/api/internal/roles"
	"example.com/todo/api/internal/routes"
	"example.com/todo/api/openapi"
	"github.com/stopyellingatme/spinup/kit/app"
	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/config"
	"github.com/stopyellingatme/spinup/kit/db"
	"github.com/stopyellingatme/spinup/kit/telemetry"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (err error) {
	var cfg config.Base
	if err := config.Load(&cfg); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdown, err := telemetry.Setup(ctx, telemetry.Options{ServiceName: "todo-api", ServiceVersion: version, Environment: cfg.Env})
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		err = errors.Join(err, shutdown(shutdownCtx))
	}()
	pool, err := db.Open(ctx, cfg.DatabaseURL, db.WithMaxConns(cfg.DatabaseMaxConns))
	if err != nil {
		return err
	}
	// The web app posts its form actions from PUBLIC_ORIGIN, so the CSRF check trusts it.
	var web struct {
		PublicOrigin string `env:"PUBLIC_ORIGIN" default:"http://localhost:5173"`
	}
	if err := config.Load(&web); err != nil {
		return err
	}
	var apikeysEnv struct {
		Flush time.Duration `env:"APIKEYS_FLUSH" default:"5s"`
	}
	if err := config.Load(&apikeysEnv); err != nil {
		return err
	}
	keyStore, err := apikeys.NewStore(pool, appapikeys.Config, apikeys.WithRPCPath(kitmcp.Path))
	if err != nil {
		return err
	}
	if err := keyStore.Start(ctx, apikeysEnv.Flush); err != nil {
		return err
	}
	mcpServer, err := appmcp.New(appmcp.Deps{DB: pool, Version: version})
	if err != nil {
		return err
	}
	authn, err := auth.New(pool,
		auth.WithRoles(roles.Set),
		auth.WithBearer(keyStore),
		auth.WithRegistration(auth.RegistrationMode(os.Getenv("AUTH_REGISTRATION"))),
		auth.WithSecureCookie(cfg.Env != "dev"),
		auth.WithTrustedOrigins(web.PublicOrigin),
	)
	if err != nil {
		return err
	}
	a := app.New(
		app.WithName("todo"),
		app.WithVersion(version),
		app.WithConfig(cfg),
		app.WithAPIDocs(openapi.FS),
		app.WithRoutes(func(mux *http.ServeMux) { routes.Register(mux, routes.Deps{DB: pool}) }),
		app.WithMiddleware(authn.Middleware()),
		app.WithMiddleware(keyStore.Middleware()),
		app.WithRoutes(authn.Routes),
		app.WithCommand("user", auth.UserCommand(authn)),
		app.WithRoutes(keyStore.Routes(authn)),
		app.WithCommand("apikeys", keyStore.Command()),
		app.WithReadiness("apikeys", keyStore.Ready),
		app.WithRoutes(mcpServer.Routes(web.PublicOrigin)),
		app.WithCommand("mcp", mcpServer.Command()),
		app.WithReadiness("db", pool.Ping),
		app.WithCommand("migrate", db.MigrateCommand(migrations.FS, db.WithDatabaseURL(cfg.DatabaseURL), db.WithKitMigrations(kitmigrations.FS))),
		app.WithShutdown(func(context.Context) error { pool.Close(); return nil }),
		app.WithShutdown(authn.Close),
		app.WithShutdown(keyStore.Close),
	)
	return a.Run(ctx, os.Args[1:])
}
