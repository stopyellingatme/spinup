package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

// DefaultQueue is the queue that jobs use when no queue is given.
const DefaultQueue = river.QueueDefault

// DefaultQueueWorkers is the number of jobs that a worker runs at the same
// time on [DefaultQueue] when no [WithQueue] option is given.
const DefaultQueueWorkers = 10

// Args are the arguments of one job kind. River stores them as JSON, so use
// exported fields with json tags. Kind returns a stable name for the kind,
// such as "send_digest". Do not change it after jobs of the kind exist.
type Args interface {
	Kind() string
}

// Job is one job that a worker function runs.
type Job[T Args] struct {
	// ID is the River job ID.
	ID int64
	// Args are the decoded job arguments.
	Args T
	// Attempt is the attempt number. The first attempt is 1.
	Attempt int
}

// Option changes how [New] and [NewWorker] configure River.
type Option func(*config)

type config struct {
	logger       *slog.Logger
	queues       map[string]int
	maxAttempts  int
	pollInterval time.Duration
}

// WithLogger sets the logger for River (default slog.Default()).
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}

// WithQueue makes a worker take jobs from the queue name, with up to
// maxWorkers jobs at the same time. Give it more than once for more queues.
// Without it, a worker takes jobs from [DefaultQueue] only.
func WithQueue(name string, maxWorkers int) Option {
	return func(c *config) {
		if c.queues == nil {
			c.queues = map[string]int{}
		}
		c.queues[name] = maxWorkers
	}
}

// WithMaxAttempts sets the default number of attempts for a job (River's
// default is 25). The [MaxAttempts] insert option overrides it.
func WithMaxAttempts(n int) Option {
	return func(c *config) { c.maxAttempts = n }
}

// WithPollInterval sets how often a worker polls for jobs when it gets no
// notification from PostgreSQL (River's default is one second).
func WithPollInterval(d time.Duration) Option {
	return func(c *config) { c.pollInterval = d }
}

func newConfig(opts []Option) config {
	c := config{logger: slog.Default()}
	for _, o := range opts {
		o(&c)
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	return c
}

func (c config) river() *river.Config {
	rc := &river.Config{
		Logger:            c.logger,
		MaxAttempts:       c.maxAttempts,
		FetchPollInterval: c.pollInterval,
	}
	// River refuses a poll interval below its fetch cooldown.
	if c.pollInterval > 0 && c.pollInterval < river.FetchCooldownDefault {
		rc.FetchCooldown = c.pollInterval
	}
	return rc
}

// Client inserts jobs. It does not work them. Use it in the API process.
// It is safe for concurrent use.
type Client struct {
	river *river.Client[pgx.Tx]
}

// New returns an insert-only client on pool. It does not connect until a
// method runs. The River tables must exist (see [MigrationHook]).
func New(pool *pgxpool.Pool, opts ...Option) (*Client, error) {
	if pool == nil {
		return nil, &userError{what: "jobs: create the client", why: "the database pool is nil", try: "pass the pool from db.Open"}
	}
	cfg := newConfig(opts)
	rc, err := river.NewClient(riverpgxv5.New(pool), cfg.river())
	if err != nil {
		return nil, &userError{what: "jobs: create the client", why: err.Error(), try: "check the jobs options", err: err}
	}
	return &Client{river: rc}, nil
}

// InsertOption changes one job insert.
type InsertOption func(*river.InsertOpts)

// Queue puts the job on the queue name instead of [DefaultQueue].
func Queue(name string) InsertOption {
	return func(o *river.InsertOpts) { o.Queue = name }
}

// Priority sets the job priority, from 1 (the highest, the default) to 4.
func Priority(n int) InsertOption {
	return func(o *river.InsertOpts) { o.Priority = n }
}

// RunAt makes the job run at t or later, instead of now.
func RunAt(t time.Time) InsertOption {
	return func(o *river.InsertOpts) { o.ScheduledAt = t }
}

// MaxAttempts sets the number of attempts before River discards the job.
func MaxAttempts(n int) InsertOption {
	return func(o *river.InsertOpts) { o.MaxAttempts = n }
}

// UniqueByArgs skips the insert when a job of the same kind with the same
// args is already waiting or running. The insert then returns the ID of the
// job that exists.
func UniqueByArgs() InsertOption {
	return func(o *river.InsertOpts) { o.UniqueOpts.ByArgs = true }
}

func insertOpts(opts []InsertOption) *river.InsertOpts {
	if len(opts) == 0 {
		return nil
	}
	o := &river.InsertOpts{}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// Insert adds a job and returns its ID.
func (c *Client) Insert(ctx context.Context, args Args, opts ...InsertOption) (int64, error) {
	return insert(ctx, c.river, nil, args, opts)
}

// InsertTx adds a job in the transaction tx and returns its ID. The job is
// visible to workers only after tx commits.
func (c *Client) InsertTx(ctx context.Context, tx pgx.Tx, args Args, opts ...InsertOption) (int64, error) {
	if tx == nil {
		return 0, &userError{what: "jobs: insert a job", why: "the transaction is nil", try: "pass the pgx.Tx from pool.Begin, or call Insert"}
	}
	return insert(ctx, c.river, tx, args, opts)
}

func insert(ctx context.Context, rc *river.Client[pgx.Tx], tx pgx.Tx, args Args, opts []InsertOption) (int64, error) {
	if args == nil {
		return 0, &userError{what: "jobs: insert a job", why: "the args are nil", try: "pass a job args value, such as SendDigestArgs{}"}
	}
	do := func() (*rivertype.JobInsertResult, error) { return rc.Insert(ctx, args, insertOpts(opts)) }
	if tx != nil {
		do = func() (*rivertype.JobInsertResult, error) { return rc.InsertTx(ctx, tx, args, insertOpts(opts)) }
	}
	res, err := do()
	if err != nil {
		return 0, insertError(args.Kind(), err)
	}
	return res.Job.ID, nil
}

func insertError(kind string, err error) error {
	try := "check the job args and the insert options"
	if isUndefinedTable(err) {
		try = "run the River migrations: go run ./cmd/api migrate up"
	}
	return &userError{
		what: fmt.Sprintf("jobs: insert a %q job", kind),
		why:  err.Error(),
		try:  try,
		err:  err,
	}
}

// isUndefinedTable reports whether err is the PostgreSQL error for a table
// that does not exist (SQLSTATE 42P01).
func isUndefinedTable(err error) bool {
	var coded interface{ SQLState() string }
	return errors.As(err, &coded) && coded.SQLState() == "42P01"
}
