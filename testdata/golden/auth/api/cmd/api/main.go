package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"example.com/todo/api/db/migrations"
	"example.com/todo/api/internal/routes"
	"github.com/stopyellingatme/spinup/kit/app"
	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/config"
	"github.com/stopyellingatme/spinup/kit/db"
)

var version = "dev"

func main() {
	var cfg config.Base
	if err := config.Load(&cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pool, err := db.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// The web app posts its form actions from PUBLIC_ORIGIN, so the CSRF check trusts it.
	var web struct {
		PublicOrigin string `env:"PUBLIC_ORIGIN" default:"http://localhost:5173"`
	}
	if err := config.Load(&web); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	authn, err := auth.New(pool, auth.WithSecureCookie(cfg.Env != "dev"), auth.WithTrustedOrigins(web.PublicOrigin))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	a := app.New(
		app.WithName("todo"),
		app.WithVersion(version),
		app.WithConfig(cfg),
		app.WithRoutes(func(mux *http.ServeMux) { routes.Register(mux, routes.Deps{DB: pool}) }),
		app.WithMiddleware(authn.Middleware()),
		app.WithRoutes(authn.Routes),
		app.WithReadiness("db", pool.Ping),
		app.WithCommand("migrate", db.MigrateCommand(migrations.FS, db.WithDatabaseURL(cfg.DatabaseURL))),
		app.WithShutdown(func(context.Context) error { pool.Close(); return nil }),
		app.WithShutdown(authn.Close),
	)
	app.Main(a)
}
