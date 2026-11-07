// Command worker runs the background jobs of the app.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	appapikeys "example.com/todo/api/internal/apikeys"
	appbilling "example.com/todo/api/internal/billing"
	jobsapp "example.com/todo/api/internal/jobs"
	"example.com/todo/api/internal/roles"
	"github.com/stopyellingatme/spinup/kit/config"
	"github.com/stopyellingatme/spinup/kit/db"
	"github.com/stopyellingatme/spinup/kit/jobs"
	"github.com/stopyellingatme/spinup/kit/logx"
	"github.com/stopyellingatme/spinup/kit/x/apikeys"
	kitbilling "github.com/stopyellingatme/spinup/kit/x/billing"
	"github.com/stopyellingatme/spinup/kit/x/billing/stripe"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var cfg config.Base
	if err := config.Load(&cfg); err != nil {
		return err
	}
	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	logger := logx.New(os.Stderr, logx.Options{Level: level})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL, db.WithAppName("todo"+"-worker"), db.WithMaxConns(cfg.DatabaseMaxConns))
	if err != nil {
		return err
	}
	defer pool.Close()

	w, err := newWorker(pool, logger)
	if err != nil {
		return err
	}
	if err := w.Start(ctx); err != nil {
		return err
	}

	<-ctx.Done()
	logger.Info("worker: stopping", "timeout", cfg.ShutdownTimeout.String())
	stopCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	return w.Stop(stopCtx)
}

func newWorker(pool *pgxpool.Pool, logger *slog.Logger) (*jobs.Worker, error) {
	w, err := jobs.NewWorker(pool, jobs.WithLogger(logger))
	if err != nil {
		return nil, err
	}
	jobsapp.Register(w, jobsapp.Deps{DB: pool})
	// Without STRIPE_SECRET_KEY the worker still starts. Then the billing sync
	// jobs fail and River retries them (ADR 0032).
	var billingEnv struct {
		SecretKey     string `env:"STRIPE_SECRET_KEY"`
		WebhookSecret string `env:"STRIPE_WEBHOOK_SECRET"`
		APIBase       string `env:"STRIPE_API_BASE"`
		PublicOrigin  string `env:"PUBLIC_ORIGIN" default:"http://localhost:5173"`
	}
	if err := config.Load(&billingEnv); err != nil {
		return nil, err
	}
	var stripeClient *stripe.Client
	if billingEnv.SecretKey != "" {
		var stripeOptions []stripe.Option
		if billingEnv.APIBase != "" {
			stripeOptions = append(stripeOptions, stripe.WithBaseURL(billingEnv.APIBase))
		}
		stripeClient, err = stripe.New(billingEnv.SecretKey, stripeOptions...)
		if err != nil {
			return nil, err
		}
	} else {
		logger.Warn("billing: STRIPE_SECRET_KEY is not set, so the billing jobs fail. Try: set STRIPE_SECRET_KEY, then restart the worker.")
	}
	jobsClient, err := jobs.New(pool, jobs.WithLogger(logger))
	if err != nil {
		return nil, err
	}
	keyStore, err := apikeys.NewStore(pool, appapikeys.Config, apikeys.WithSessionOnly(kitbilling.SessionPrefix))
	if err != nil {
		return nil, err
	}
	billingSvc, err := kitbilling.New(pool, appbilling.Config, stripeClient,
		kitbilling.WithJobs(jobsClient),
		kitbilling.WithRoles(roles.Set),
		kitbilling.WithPublicOrigin(billingEnv.PublicOrigin),
		kitbilling.WithWebhookSecrets(billingEnv.WebhookSecret),
		kitbilling.WithLogger(logger),
		kitbilling.WithAPIKeys(keyStore),
	)
	if err != nil {
		return nil, err
	}
	billingSvc.Register(w)
	return w, nil
}
