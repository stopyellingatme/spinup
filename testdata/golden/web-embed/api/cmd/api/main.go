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
	"example.com/todo/api/internal/routes"
	"example.com/todo/api/internal/webdist"
	"github.com/stopyellingatme/spinup/kit/app"
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
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	a := app.New(
		app.WithName("todo"),
		app.WithVersion(version),
		app.WithConfig(cfg),
		app.WithSPA(webdist.FS),
		app.WithRoutes(func(mux *http.ServeMux) { routes.Register(mux, routes.Deps{DB: pool}) }),
		app.WithReadiness("db", pool.Ping),
		app.WithCommand("migrate", db.MigrateCommand(migrations.FS, db.WithDatabaseURL(cfg.DatabaseURL))),
		app.WithShutdown(func(context.Context) error { pool.Close(); return nil }),
	)
	return a.Run(ctx, os.Args[1:])
}
