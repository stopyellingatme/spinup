package db

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Default runtime parameters for application connections.
const (
	DefaultStatementTimeout = 30 * time.Second
	DefaultLockTimeout      = 5 * time.Second
	DefaultAppName          = "spinup-app"
)

// Option changes how [Open] configures the pool.
type Option func(*openConfig)

type openConfig struct {
	statementTimeout *time.Duration
	lockTimeout      *time.Duration
	appName          *string
	maxConns         int32
}

// WithStatementTimeout sets statement_timeout on every connection.
// Zero turns the timeout off.
func WithStatementTimeout(d time.Duration) Option {
	return func(c *openConfig) { c.statementTimeout = &d }
}

// WithLockTimeout sets lock_timeout on every connection. Zero turns the timeout off.
func WithLockTimeout(d time.Duration) Option {
	return func(c *openConfig) { c.lockTimeout = &d }
}

// WithAppName sets application_name on every connection.
func WithAppName(name string) Option {
	return func(c *openConfig) { c.appName = &name }
}

// WithMaxConns sets the maximum pool size. Zero or less keeps the pgx default.
func WithMaxConns(n int32) Option {
	return func(c *openConfig) { c.maxConns = n }
}

// Open parses url and returns a pgx pool. It does not connect: the pool
// connects on first use, so an API can start and serve /healthz while the
// database is down.
//
// Each connection gets statement_timeout (default 30s), lock_timeout
// (default 5s), and application_name (default "spinup-app"). An option
// overrides a value. Without an option, a value in the URL query wins over
// the default.
func Open(ctx context.Context, url string, opts ...Option) (*pgxpool.Pool, error) {
	const what = "db: open the database pool"
	if url == "" {
		return nil, errEmptyURL(what)
	}
	var oc openConfig
	for _, o := range opts {
		o(&oc)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errParseURL(what)
	}
	params := cfg.ConnConfig.RuntimeParams
	setParam(params, "statement_timeout", durationParam(oc.statementTimeout), durationValue(DefaultStatementTimeout))
	setParam(params, "lock_timeout", durationParam(oc.lockTimeout), durationValue(DefaultLockTimeout))
	setParam(params, "application_name", oc.appName, DefaultAppName)
	if oc.maxConns > 0 {
		cfg.MaxConns = oc.maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, &userError{what: what, why: err.Error(), try: "check DATABASE_URL", err: err}
	}
	return pool, nil
}

// setParam sets params[key] to the explicit value when it is not nil. Otherwise
// it keeps a value from the URL, or sets the default.
func setParam(params map[string]string, key string, explicit *string, def string) {
	switch {
	case explicit != nil:
		params[key] = *explicit
	case params[key] == "":
		params[key] = def
	}
}

// durationParam converts an optional duration to a Postgres parameter value.
func durationParam(d *time.Duration) *string {
	if d == nil {
		return nil
	}
	v := durationValue(*d)
	return &v
}

// durationValue formats d as whole milliseconds. Postgres reads a timeout
// parameter with no unit suffix as milliseconds.
func durationValue(d time.Duration) string {
	return strconv.FormatInt(d.Milliseconds(), 10)
}
