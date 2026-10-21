package db

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Default runtime parameters for application connections.
const (
	DefaultStatementTimeout = 30 * time.Second
	DefaultLockTimeout      = 5 * time.Second
	DefaultAppName          = "spinup-app"
	// DefaultMaxConns is the pool size when neither an option nor the URL sets one.
	// The pgx default, max(4, NumCPU), was the first queue in the load test:
	// requests waited for a connection while the API and Postgres had idle CPU.
	// Two colors with two API replicas and a worker each stay far below the
	// max_connections of 200 in the deploy Postgres.
	DefaultMaxConns = 20
)

// Option changes how [Open] configures the pool.
type Option func(*openConfig)

type openConfig struct {
	statementTimeout *time.Duration
	lockTimeout      *time.Duration
	appName          *string
	maxConns         int
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

// WithMaxConns sets the maximum pool size. It wins over pool_max_conns in the
// URL. Zero keeps the URL value or [DefaultMaxConns]. Open rejects a negative
// value. Pass config.Base.DatabaseMaxConns (DATABASE_MAX_CONNS) here.
func WithMaxConns(n int) Option {
	return func(c *openConfig) { c.maxConns = n }
}

// Open parses url and returns a pgx pool. It does not connect: the pool
// connects on first use, so an API can start and serve /healthz while the
// database is down.
//
// Each connection gets statement_timeout (default 30s), lock_timeout
// (default 5s), and application_name (default "spinup-app"). The pool holds
// at most [DefaultMaxConns] connections. An option overrides a value. Without
// an option, a value in the URL query (pool_max_conns for the pool size) wins
// over the default.
func Open(ctx context.Context, url string, opts ...Option) (*pgxpool.Pool, error) {
	const what = "db: open the database pool"
	if url == "" {
		return nil, errEmptyURL(what)
	}
	var oc openConfig
	for _, o := range opts {
		o(&oc)
	}
	if oc.maxConns < 0 || oc.maxConns > math.MaxInt32 {
		return nil, &userError{what: what, why: "the maximum pool size " + strconv.Itoa(oc.maxConns) + " is not valid", try: "set DATABASE_MAX_CONNS to a positive number, or remove it to use the default of " + strconv.Itoa(DefaultMaxConns)}
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errParseURL(what)
	}
	params := cfg.ConnConfig.RuntimeParams
	setParam(params, "statement_timeout", durationParam(oc.statementTimeout), durationValue(DefaultStatementTimeout))
	setParam(params, "lock_timeout", durationParam(oc.lockTimeout), durationValue(DefaultLockTimeout))
	setParam(params, "application_name", oc.appName, DefaultAppName)
	switch {
	case oc.maxConns > 0:
		cfg.MaxConns = int32(oc.maxConns)
	case !setsMaxConns(url):
		cfg.MaxConns = DefaultMaxConns
	}
	configureTelemetry(cfg)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, &userError{what: what, why: err.Error(), try: "check DATABASE_URL", err: err}
	}
	return pool, nil
}

// setsMaxConns reports whether the connection string sets pool_max_conns.
// pgconn parses every form that pgxpool accepts, quoting included, and keeps
// the pool settings, which it does not know, as runtime parameters.
func setsMaxConns(conn string) bool {
	cfg, err := pgconn.ParseConfig(conn)
	if err != nil {
		return false
	}
	_, ok := cfg.RuntimeParams["pool_max_conns"]
	return ok
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
