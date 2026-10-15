package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// Default runtime parameters for migration connections.
const (
	DefaultMigrationLockTimeout      = 5 * time.Second
	DefaultMigrationStatementTimeout = 5 * time.Minute
	migrationAppName                 = "spinup-migrate"
)

// Advisory lock probe: try once a second, for up to 15 minutes.
const (
	lockProbeSeconds  = 1
	lockProbeAttempts = 900
)

// MigrateOption changes how [NewMigrator] and [MigrateCommand] run migrations.
type MigrateOption func(*migrateConfig)

type migrateConfig struct {
	lockTimeout      time.Duration
	statementTimeout time.Duration
	out              io.Writer
	url              string
	hooks            []Hook
}

// WithMigrationLockTimeout sets lock_timeout on migration connections (default 5s).
func WithMigrationLockTimeout(d time.Duration) MigrateOption {
	return func(c *migrateConfig) { c.lockTimeout = d }
}

// WithMigrationStatementTimeout sets statement_timeout on migration connections (default 5m).
func WithMigrationStatementTimeout(d time.Duration) MigrateOption {
	return func(c *migrateConfig) { c.statementTimeout = d }
}

// WithOutput sets where the migrator writes its progress lines (default os.Stdout).
func WithOutput(w io.Writer) MigrateOption {
	return func(c *migrateConfig) { c.out = w }
}

// WithDatabaseURL sets the database URL. [NewMigrator] uses it when its url
// argument is empty. [MigrateCommand] uses it before DATABASE_URL.
func WithDatabaseURL(url string) MigrateOption {
	return func(c *migrateConfig) { c.url = url }
}

// Hook runs extra migration steps in the same `migrate` command, for
// example the migrations of a job queue. [MigrateCommand] calls AfterUp
// after the app migrations move up, and BeforeDown before they move down.
// Both functions get the database URL. A nil function is skipped.
type Hook struct {
	AfterUp    func(ctx context.Context, url string) error
	BeforeDown func(ctx context.Context, url string) error
}

// WithHook adds a migration hook to [MigrateCommand]. Give it more than once
// to add more hooks. The hooks run in the order given.
func WithHook(h Hook) MigrateOption {
	return func(c *migrateConfig) { c.hooks = append(c.hooks, h) }
}

func newMigrateConfig(opts []MigrateOption) migrateConfig {
	c := migrateConfig{
		lockTimeout:      DefaultMigrationLockTimeout,
		statementTimeout: DefaultMigrationStatementTimeout,
		out:              os.Stdout,
	}
	for _, o := range opts {
		o(&c)
	}
	return c
}

// MigrationStatus is the state of one migration file.
type MigrationStatus struct {
	Name    string // file name, for example "00001_init.sql"
	Applied bool
}

// Migrator runs the SQL migrations in a file system against one database.
// A Postgres advisory lock serializes runners, so two processes never
// migrate the same database at once.
type Migrator struct {
	db       *sql.DB
	provider *goose.Provider // nil when fsys holds no migrations
	out      io.Writer
}

// NewMigrator returns a migrator for the database at url and the *.sql files
// at the root of fsys. When url is empty, it uses [WithDatabaseURL]. It does
// not connect until a method runs. Call [Migrator.Close] when done.
func NewMigrator(url string, fsys fs.FS, opts ...MigrateOption) (*Migrator, error) {
	const what = "db: prepare migrations"
	cfg := newMigrateConfig(opts)
	if url == "" {
		url = cfg.url
	}
	if url == "" {
		return nil, errEmptyURL(what)
	}
	connCfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, errParseURL(what)
	}
	connCfg.RuntimeParams["lock_timeout"] = durationValue(cfg.lockTimeout)
	connCfg.RuntimeParams["statement_timeout"] = durationValue(cfg.statementTimeout)
	if connCfg.RuntimeParams["application_name"] == "" {
		connCfg.RuntimeParams["application_name"] = migrationAppName
	}
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(lockProbeSeconds, lockProbeAttempts))
	if err != nil {
		return nil, fmt.Errorf("%s: create the advisory locker: %w", what, err)
	}
	sqlDB := stdlib.OpenDB(*connCfg)
	m := &Migrator{db: sqlDB, out: cfg.out}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys,
		goose.WithSessionLocker(locker),
		goose.WithDisableGlobalRegistry(true),
	)
	switch {
	case errors.Is(err, goose.ErrNoMigrations):
		// An app with no migrations yet is valid. Every method reports nothing to do.
	case err != nil:
		_ = sqlDB.Close()
		return nil, &userError{
			what: what,
			why:  err.Error(),
			try:  "check the file names in the migrations directory (NNNNN_name.sql)",
			err:  err,
		}
	default:
		m.provider = provider
	}
	return m, nil
}

// Close closes the database handle.
func (m *Migrator) Close() error {
	return m.db.Close()
}

// Status returns every migration file in version order, with its state.
func (m *Migrator) Status(ctx context.Context) ([]MigrationStatus, error) {
	if m.provider == nil {
		return nil, nil
	}
	list, err := m.provider.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("db: read migration status: %w", err)
	}
	out := make([]MigrationStatus, 0, len(list))
	for _, s := range list {
		out = append(out, MigrationStatus{
			Name:    path.Base(s.Source.Path),
			Applied: s.State == goose.StateApplied,
		})
	}
	return out, nil
}

// Pending returns the names of the migrations that are not applied, in version order.
func (m *Migrator) Pending(ctx context.Context) ([]string, error) {
	list, err := m.Status(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, s := range list {
		if !s.Applied {
			names = append(names, s.Name)
		}
	}
	return names, nil
}

// Up applies every pending migration. It writes the plan first, either
// "migrate: no pending migrations" or "migrate: N pending", and then one
// "applied  <file>" line for each migration it applies. When another runner
// applied the pending migrations while this one waited on the lock, it writes
// "migrate: another runner applied them".
func (m *Migrator) Up(ctx context.Context) error {
	pending, err := m.Pending(ctx)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		m.printf("migrate: no pending migrations\n")
		return nil
	}
	m.printf("migrate: %d pending\n", len(pending))
	results, err := m.provider.Up(ctx)
	var partial *goose.PartialError
	if errors.As(err, &partial) {
		results = partial.Applied
	}
	for _, r := range results {
		m.printf("applied  %s\n", path.Base(r.Source.Path))
	}
	if err != nil {
		return fmt.Errorf("db: apply migrations: %w", err)
	}
	if len(results) == 0 {
		// Another runner held the advisory lock and applied them first.
		m.printf("migrate: another runner applied them\n")
	}
	return nil
}

// Down rolls back the most recent migration. It writes "rolled back  <file>",
// or "migrate: nothing to roll back".
func (m *Migrator) Down(ctx context.Context) error {
	if m.provider == nil {
		m.printf("migrate: nothing to roll back\n")
		return nil
	}
	res, err := m.provider.Down(ctx)
	if errors.Is(err, goose.ErrNoNextVersion) {
		m.printf("migrate: nothing to roll back\n")
		return nil
	}
	if err != nil {
		return fmt.Errorf("db: roll back migration: %w", err)
	}
	m.printf("rolled back  %s\n", path.Base(res.Source.Path))
	return nil
}

// Version returns the highest applied migration version, or 0 when no
// migration is applied.
func (m *Migrator) Version(ctx context.Context) (int64, error) {
	if m.provider == nil {
		return 0, nil
	}
	v, err := m.provider.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("db: read migration version: %w", err)
	}
	return v, nil
}

// Redo rolls back the most recent migration and applies it again. It writes
// "rolled back  <file>" and "applied  <file>", or "migrate: nothing to redo".
func (m *Migrator) Redo(ctx context.Context) error {
	if m.provider == nil {
		m.printf("migrate: nothing to redo\n")
		return nil
	}
	res, err := m.provider.Down(ctx)
	if errors.Is(err, goose.ErrNoNextVersion) {
		m.printf("migrate: nothing to redo\n")
		return nil
	}
	if err != nil {
		return fmt.Errorf("db: roll back migration: %w", err)
	}
	m.printf("rolled back  %s\n", path.Base(res.Source.Path))
	res, err = m.provider.ApplyVersion(ctx, res.Source.Version, true)
	if err != nil {
		return fmt.Errorf("db: apply migration again: %w", err)
	}
	m.printf("applied  %s\n", path.Base(res.Source.Path))
	return nil
}

// HasVersion reports whether a migration file has the version v.
func (m *Migrator) HasVersion(v int64) bool {
	if m.provider == nil {
		return false
	}
	for _, s := range m.provider.ListSources() {
		if s.Version == v {
			return true
		}
	}
	return false
}

// UpTo applies the pending migrations up to and including version v. It
// writes one "applied  <file>" line for each migration it applies.
func (m *Migrator) UpTo(ctx context.Context, v int64) error {
	if m.provider == nil {
		return nil
	}
	results, err := m.provider.UpTo(ctx, v)
	var partial *goose.PartialError
	if errors.As(err, &partial) {
		results = partial.Applied
	}
	for _, r := range results {
		m.printf("applied  %s\n", path.Base(r.Source.Path))
	}
	if err != nil {
		return fmt.Errorf("db: apply migrations up to version %d: %w", v, err)
	}
	return nil
}

// DownTo rolls back the applied migrations above version v, newest first.
// It writes one "rolled back  <file>" line for each migration.
func (m *Migrator) DownTo(ctx context.Context, v int64) error {
	if m.provider == nil {
		return nil
	}
	results, err := m.provider.DownTo(ctx, v)
	var partial *goose.PartialError
	if errors.As(err, &partial) {
		results = partial.Applied
	}
	for _, r := range results {
		m.printf("rolled back  %s\n", path.Base(r.Source.Path))
	}
	if err != nil {
		return fmt.Errorf("db: roll back migrations to version %d: %w", v, err)
	}
	return nil
}

func (m *Migrator) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(m.out, format, args...)
}
