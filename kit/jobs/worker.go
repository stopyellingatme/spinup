package jobs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// hardStopTimeout limits how long [Worker.Stop] waits for cancelled jobs
// after its context ends.
const hardStopTimeout = 5 * time.Second

// Worker works jobs. Run it in its own process (cmd/worker). Call [Register]
// and [Worker.Periodic] before [Worker.Start].
type Worker struct {
	pool    *pgxpool.Pool
	cfg     config
	workers *river.Workers
	kinds   []string

	mu       sync.Mutex
	periodic []*river.PeriodicJob
	client   *river.Client[pgx.Tx]
	started  bool
}

// NewWorker returns a worker on pool. It does not connect until
// [Worker.Start]. The River tables must exist (see [MigrationHook]).
func NewWorker(pool *pgxpool.Pool, opts ...Option) (*Worker, error) {
	if pool == nil {
		return nil, &userError{what: "jobs: create the worker", why: "the database pool is nil", try: "pass the pool from db.Open"}
	}
	cfg := newConfig(opts)
	for name, n := range cfg.queues {
		if name == "" || n < 1 {
			return nil, &userError{
				what: "jobs: create the worker",
				why:  fmt.Sprintf("queue %q has %d workers (want a name and at least 1 worker)", name, n),
				try:  `jobs.WithQueue("default", 10)`,
			}
		}
	}
	return &Worker{pool: pool, cfg: cfg, workers: river.NewWorkers()}, nil
}

// Register adds fn as the function that works jobs of the kind T. Call it
// before [Worker.Start]. It panics when the kind is already registered or
// the worker has started, because both are programming errors.
func Register[T Args](w *Worker, fn func(ctx context.Context, job Job[T]) error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var zero T
	kind := zero.Kind()
	if w.started {
		panic(fmt.Sprintf("jobs: Register(%q) after Start: register every job before Start", kind))
	}
	for _, k := range w.kinds {
		if k == kind {
			panic(fmt.Sprintf("jobs: job kind %q is registered twice", kind))
		}
	}
	w.kinds = append(w.kinds, kind)
	river.AddWorker(w.workers, river.WorkFunc(func(ctx context.Context, j *river.Job[T]) error {
		return fn(ctx, Job[T]{ID: j.ID, Args: j.Args, Attempt: j.Attempt})
	}))
}

// Kinds returns the registered job kinds, sorted.
func (w *Worker) Kinds() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	kinds := append([]string(nil), w.kinds...)
	sort.Strings(kinds)
	return kinds
}

// Periodic inserts a job with args every interval while the worker runs.
// Only one worker process (the River leader) inserts periodic jobs, so more
// worker processes do not insert more jobs. Call it before [Worker.Start].
// It panics when the worker has started or every is not positive.
func (w *Worker) Periodic(every time.Duration, args Args) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		panic("jobs: Periodic after Start: add every periodic job before Start")
	}
	if every <= 0 || args == nil {
		panic(fmt.Sprintf("jobs: Periodic needs a positive interval and args (got %s, %v)", every, args))
	}
	w.periodic = append(w.periodic, river.NewPeriodicJob(
		river.PeriodicInterval(every),
		func() (river.JobArgs, *river.InsertOpts) { return args, nil },
		nil,
	))
}

// Start starts working jobs in the background and returns. A worker with no
// registered job logs a warning and does nothing. Cancelling ctx does not
// stop the worker: call [Worker.Stop].
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return &userError{what: "jobs: start the worker", why: "the worker has already started", try: "call Start once"}
	}
	w.started = true
	if len(w.kinds) == 0 {
		w.cfg.logger.WarnContext(ctx, "jobs: no job is registered, the worker does nothing", "try", "spinup g job <name>")
		return nil
	}
	rc := w.cfg.river()
	rc.Workers = w.workers
	rc.PeriodicJobs = w.periodic
	rc.Queues = map[string]river.QueueConfig{}
	for name, n := range w.cfg.queues {
		rc.Queues[name] = river.QueueConfig{MaxWorkers: n}
	}
	if len(rc.Queues) == 0 {
		rc.Queues[DefaultQueue] = river.QueueConfig{MaxWorkers: DefaultQueueWorkers}
	}
	client, err := river.NewClient(riverpgxv5.New(w.pool), rc)
	if err != nil {
		return &userError{what: "jobs: start the worker", why: err.Error(), try: "check the jobs options", err: err}
	}
	// Only Stop ends the worker, so a cancelled ctx never cuts a job short.
	if err := client.Start(context.WithoutCancel(ctx)); err != nil {
		try := "check DATABASE_URL and that PostgreSQL runs"
		if isUndefinedTable(err) {
			try = "run the River migrations: go run ./cmd/api migrate up"
		}
		return &userError{what: "jobs: start the worker", why: err.Error(), try: try, err: err}
	}
	w.client = client
	w.cfg.logger.InfoContext(ctx, "jobs: worker started", "kinds", w.kinds, "periodic", len(w.periodic))
	return nil
}

// Stop stops the worker gracefully. It stops taking new jobs and waits for
// the running jobs until ctx ends. Then it cancels the jobs that still run
// and returns ctx's error. Stop on a worker that did not start does nothing.
func (w *Worker) Stop(ctx context.Context) error {
	w.mu.Lock()
	client := w.client
	w.mu.Unlock()
	if client == nil {
		return nil
	}
	err := client.Stop(ctx)
	if err == nil {
		w.cfg.logger.InfoContext(ctx, "jobs: worker stopped")
		return nil
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("jobs: stop the worker: %w", err)
	}
	hard, cancel := context.WithTimeout(context.WithoutCancel(ctx), hardStopTimeout)
	defer cancel()
	_ = client.StopAndCancel(hard)
	return &userError{
		what: "jobs: stop the worker",
		why:  "running jobs did not finish in time and were cancelled",
		try:  "give Stop a longer timeout, or make long jobs check ctx.Done()",
		err:  err,
	}
}
