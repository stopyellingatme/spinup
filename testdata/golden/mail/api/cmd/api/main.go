package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"example.com/todo/api/db/migrations"
	mailapp "example.com/todo/api/internal/mail"
	"example.com/todo/api/internal/routes"
	"example.com/todo/api/openapi"
	"github.com/stopyellingatme/spinup/kit/app"
	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/config"
	"github.com/stopyellingatme/spinup/kit/db"
	"github.com/stopyellingatme/spinup/kit/jobs"
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
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	mailers, err := mailapp.New(pool)
	if err != nil {
		return err
	}
	_ = mailers // Pass this dependency to application handlers that send mail.
	// The web app posts its form actions from PUBLIC_ORIGIN, so the CSRF check trusts it.
	var web struct {
		PublicOrigin string `env:"PUBLIC_ORIGIN" default:"http://localhost:5173"`
	}
	if err := config.Load(&web); err != nil {
		return err
	}
	authn, err := auth.New(pool, auth.WithSecureCookie(cfg.Env != "dev"), auth.WithTrustedOrigins(web.PublicOrigin))
	if err != nil {
		return err
	}
	resetRoutes, err := passwordResetRoutes(authn, mailers, web.PublicOrigin)
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
		app.WithRoutes(authn.Routes),
		app.WithRoutes(resetRoutes),
		app.WithCommand("mail-worker", mailers.WorkerCommand(pool)),
		app.WithReadiness("db", pool.Ping),
		app.WithCommand("migrate", db.MigrateCommand(migrations.FS, db.WithDatabaseURL(cfg.DatabaseURL), db.WithHook(jobs.MigrationHook()))),
		app.WithCommand("jobs-migrate", jobs.MigrateCommand(jobs.WithMigrateDatabaseURL(cfg.DatabaseURL))),
		app.WithShutdown(func(context.Context) error { pool.Close(); return nil }),
		app.WithShutdown(authn.Close),
	)
	return a.Run(ctx, os.Args[1:])
}
