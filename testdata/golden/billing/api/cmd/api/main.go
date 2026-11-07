package main

import (
	"context"
	"errors"
	appbilling "example.com/todo/api/internal/billing"
	"fmt"
	kitbilling "github.com/stopyellingatme/spinup/kit/x/billing"
	"github.com/stopyellingatme/spinup/kit/x/billing/stripe"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	kitmigrations "example.com/todo/api/db/kit"
	"example.com/todo/api/db/migrations"
	"example.com/todo/api/internal/roles"
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
	authn, err := auth.New(pool,
		auth.WithRoles(roles.Set),
		auth.WithRegistration(auth.RegistrationMode(os.Getenv("AUTH_REGISTRATION"))),
		auth.WithSecureCookie(cfg.Env != "dev"),
		auth.WithTrustedOrigins(web.PublicOrigin),
	)
	if err != nil {
		return err
	}
	var billingEnv struct {
		SecretKey     string `env:"STRIPE_SECRET_KEY" required:"true"`
		WebhookSecret string `env:"STRIPE_WEBHOOK_SECRET" required:"true"`
		APIBase       string `env:"STRIPE_API_BASE"`
	}
	if err := config.Load(&billingEnv); err != nil {
		return err
	}
	var stripeOptions []stripe.Option
	if billingEnv.APIBase != "" {
		stripeOptions = append(stripeOptions, stripe.WithBaseURL(billingEnv.APIBase))
	}
	stripeClient, err := stripe.New(billingEnv.SecretKey, stripeOptions...)
	if err != nil {
		return err
	}
	jobsClient, err := jobs.New(pool)
	if err != nil {
		return err
	}
	billingSvc, err := kitbilling.New(pool, appbilling.Config, stripeClient,
		kitbilling.WithJobs(jobsClient),
		kitbilling.WithRoles(roles.Set),
		kitbilling.WithPublicOrigin(web.PublicOrigin),
		kitbilling.WithWebhookSecrets(billingEnv.WebhookSecret),
		kitbilling.WithLogger(slog.Default()),
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
		app.WithRoutes(authn.Routes),
		app.WithCommand("user", auth.UserCommand(authn)),
		app.WithRoutes(billingSvc.Routes(authn)),
		app.WithCommand("billing", billingSvc.Command()),
		app.WithReadiness("db", pool.Ping),
		app.WithCommand("migrate", db.MigrateCommand(migrations.FS, db.WithDatabaseURL(cfg.DatabaseURL), db.WithKitMigrations(kitmigrations.FS), db.WithHook(jobs.MigrationHook()))),
		app.WithCommand("jobs-migrate", jobs.MigrateCommand(jobs.WithMigrateDatabaseURL(cfg.DatabaseURL))),
		app.WithShutdown(func(context.Context) error { pool.Close(); return nil }),
		app.WithShutdown(authn.Close),
	)
	return a.Run(ctx, os.Args[1:])
}
