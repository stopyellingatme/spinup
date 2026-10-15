package jobs

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/stopyellingatme/spinup/kit/db"
)

// migrationStatementTimeout limits one River migration statement.
const migrationStatementTimeout = 5 * time.Minute

const jobsMigrateUsage = "jobs-migrate up | down | status"

// MigrateOption changes how [MigrationHook] and [MigrateCommand] run.
type MigrateOption func(*migrateConfig)

type migrateConfig struct {
	out io.Writer
	url string
}

// WithMigrateOutput sets where the River migrations write their progress
// lines (default os.Stdout).
func WithMigrateOutput(w io.Writer) MigrateOption {
	return func(c *migrateConfig) { c.out = w }
}

// WithMigrateDatabaseURL sets the database URL of [MigrateCommand]. Without
// it, the command reads DATABASE_URL. [MigrationHook] ignores it, because
// the migrate command gives the hook its URL.
func WithMigrateDatabaseURL(url string) MigrateOption {
	return func(c *migrateConfig) { c.url = url }
}

func newMigrateConfig(opts []MigrateOption) migrateConfig {
	c := migrateConfig{out: os.Stdout}
	for _, o := range opts {
		o(&c)
	}
	if c.out == nil {
		c.out = io.Discard
	}
	return c
}

// MigrationHook returns a kit db hook that runs the River migrations in the
// app's `migrate` step:
//
//	db.MigrateCommand(migrations.FS, db.WithHook(jobs.MigrationHook()))
//
// AfterUp migrates River up to its latest version. It writes
// "river: up to date" or one "applied  river <version> <name>" line for each
// version. BeforeDown does nothing: an app rollback keeps the job tables.
// Roll River back with the explicit [MigrateCommand] instead.
func MigrationHook(opts ...MigrateOption) db.Hook {
	cfg := newMigrateConfig(opts)
	return db.Hook{
		AfterUp: func(ctx context.Context, url string) error {
			return withMigrator(ctx, url, func(m *rivermigrate.Migrator[pgx.Tx]) error {
				return migrateUp(ctx, m, cfg.out)
			})
		},
	}
}

// MigrateCommand returns the app command "jobs-migrate". The generated
// main.go registers it, so `go run ./cmd/api jobs-migrate <subcommand>` runs
// the River migrations by hand:
//
//	up:     "river: up to date", or one "applied  river <version> <name>" line for each version
//	down:   "rolled back  river <version> <name>" (one version), or "river: nothing to roll back"
//	status: one "applied  river <version> <name>" or "pending  river <version> <name>" line for each version
//
// The database URL comes from [WithMigrateDatabaseURL], else from DATABASE_URL.
func MigrateCommand(opts ...MigrateOption) func(ctx context.Context, args []string) error {
	cfg := newMigrateConfig(opts)
	return func(ctx context.Context, args []string) error {
		sub := ""
		if len(args) > 0 {
			sub = args[0]
		}
		switch sub {
		case "up", "down", "status":
		case "":
			return &userError{what: "jobs-migrate", why: "no subcommand given (want up, down, or status)", try: jobsMigrateUsage}
		default:
			return &userError{what: "jobs-migrate", why: fmt.Sprintf("unknown subcommand %q (want up, down, or status)", sub), try: jobsMigrateUsage}
		}
		if len(args) > 1 {
			return &userError{what: "jobs-migrate " + sub, why: fmt.Sprintf("unexpected arguments %q", args[1:]), try: "jobs-migrate " + sub}
		}
		url := cfg.url
		if url == "" {
			url = os.Getenv("DATABASE_URL")
		}
		return withMigrator(ctx, url, func(m *rivermigrate.Migrator[pgx.Tx]) error {
			switch sub {
			case "up":
				return migrateUp(ctx, m, cfg.out)
			case "down":
				return migrateDown(ctx, m, cfg.out)
			default:
				return migrateStatus(ctx, m, cfg.out)
			}
		})
	}
}

// withMigrator opens a pool on url and runs fn with a River migrator.
func withMigrator(ctx context.Context, url string, fn func(*rivermigrate.Migrator[pgx.Tx]) error) error {
	pool, err := db.Open(ctx, url,
		db.WithAppName("spinup-migrate"),
		db.WithStatementTimeout(migrationStatementTimeout),
		db.WithMaxConns(2),
	)
	if err != nil {
		return err
	}
	defer pool.Close()
	m, err := newMigrator(pool)
	if err != nil {
		return err
	}
	return fn(m)
}

func newMigrator(pool *pgxpool.Pool) (*rivermigrate.Migrator[pgx.Tx], error) {
	m, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{})
	if err != nil {
		return nil, &userError{what: "jobs: prepare the River migrations", why: err.Error(), try: "report this as a kit bug", err: err}
	}
	return m, nil
}

func migrateUp(ctx context.Context, m *rivermigrate.Migrator[pgx.Tx], out io.Writer) error {
	res, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return &userError{what: "jobs: apply the River migrations", why: err.Error(), try: "check that the database user can create tables, then run migrate up again", err: err}
	}
	if len(res.Versions) == 0 {
		_, _ = fmt.Fprintln(out, "river: up to date")
		return nil
	}
	for _, v := range res.Versions {
		_, _ = fmt.Fprintf(out, "applied  river %03d %s\n", v.Version, v.Name)
	}
	return nil
}

func migrateDown(ctx context.Context, m *rivermigrate.Migrator[pgx.Tx], out io.Writer) error {
	existing, err := m.ExistingVersions(ctx)
	if err != nil {
		return statusError(err)
	}
	if len(existing) == 0 {
		_, _ = fmt.Fprintln(out, "river: nothing to roll back")
		return nil
	}
	res, err := m.Migrate(ctx, rivermigrate.DirectionDown, &rivermigrate.MigrateOpts{MaxSteps: 1})
	if err != nil {
		return &userError{what: "jobs: roll back a River migration", why: err.Error(), try: "jobs-migrate status, then fix the cause and run jobs-migrate down again", err: err}
	}
	for _, v := range res.Versions {
		_, _ = fmt.Fprintf(out, "rolled back  river %03d %s\n", v.Version, v.Name)
	}
	return nil
}

func migrateStatus(ctx context.Context, m *rivermigrate.Migrator[pgx.Tx], out io.Writer) error {
	existing, err := m.ExistingVersions(ctx)
	if err != nil {
		return statusError(err)
	}
	applied := map[int]bool{}
	for _, v := range existing {
		applied[v.Version] = true
	}
	for _, v := range m.AllVersions() {
		state := "pending"
		if applied[v.Version] {
			state = "applied"
		}
		_, _ = fmt.Fprintf(out, "%s  river %03d %s\n", state, v.Version, v.Name)
	}
	return nil
}

func statusError(err error) error {
	return &userError{what: "jobs: read the River migration status", why: err.Error(), try: "check DATABASE_URL and that PostgreSQL runs", err: err}
}
