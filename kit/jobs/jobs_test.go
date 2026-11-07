package jobs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/jobs"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

const waitTimeout = 30 * time.Second

type greetArgs struct {
	Name string `json:"name"`
}

func (greetArgs) Kind() string { return "greet" }

type tickArgs struct{}

func (tickArgs) Kind() string { return "tick" }

type slowArgs struct{}

func (slowArgs) Kind() string { return "slow" }

func quiet() jobs.Option {
	return jobs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// migrated returns a pool on a new database with the River tables.
func migrated(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := jobsDB(t)
	var out bytes.Buffer
	hook := jobs.MigrationHook(jobs.WithMigrateOutput(&out))
	if err := hook.AfterUp(context.Background(), testkit.URL(t, pool)); err != nil {
		t.Fatal(err)
	}
	return pool
}

func jobsDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL is unset. Use the existing PostgreSQL server on port 55433.")
	}
	return testkit.DB(t)
}

// startWorker starts w and stops it when the test ends.
func startWorker(t *testing.T, w *jobs.Worker) {
	t.Helper()
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		defer cancel()
		if err := w.Stop(ctx); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
}

func TestMigrationHookAndCommand(t *testing.T) {
	t.Parallel()
	pool := jobsDB(t)
	url := testkit.URL(t, pool)
	ctx := context.Background()

	var out bytes.Buffer
	hook := jobs.MigrationHook(jobs.WithMigrateOutput(&out))
	if hook.BeforeDown != nil {
		t.Fatal("BeforeDown must do nothing: an app rollback keeps the River tables")
	}
	if err := hook.AfterUp(ctx, url); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "applied  river 001 ") {
		t.Fatalf("first up output = %q", out.String())
	}
	out.Reset()
	if err := hook.AfterUp(ctx, url); err != nil {
		t.Fatal(err)
	}
	if out.String() != "river: up to date\n" {
		t.Fatalf("second up output = %q", out.String())
	}

	run := func(args ...string) string {
		t.Helper()
		var buf bytes.Buffer
		cmd := jobs.MigrateCommand(jobs.WithMigrateDatabaseURL(url), jobs.WithMigrateOutput(&buf))
		if err := cmd(ctx, args); err != nil {
			t.Fatalf("jobs-migrate %v: %v", args, err)
		}
		return buf.String()
	}
	status := run("status")
	if strings.Contains(status, "pending  ") || !strings.Contains(status, "applied  river 001 ") {
		t.Fatalf("status after up = %q", status)
	}
	down := run("down")
	if !strings.HasPrefix(down, "rolled back  river ") || strings.Count(down, "\n") != 1 {
		t.Fatalf("down output = %q", down)
	}
	if status := run("status"); strings.Count(status, "pending  river ") != 1 {
		t.Fatalf("status after down = %q", status)
	}
	if up := run("up"); !strings.HasPrefix(up, "applied  river ") {
		t.Fatalf("up output = %q", up)
	}
}

func TestMigrateCommandUsage(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"sideways"}, {"up", "extra"}} {
		err := jobs.MigrateCommand(jobs.WithMigrateOutput(io.Discard))(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "\nTry: ") {
			t.Errorf("args %q: err = %v, want an error with a Try line", args, err)
		}
	}
}

func TestInsertAndWork(t *testing.T) {
	t.Parallel()
	pool := migrated(t)
	ctx := context.Background()

	got := make(chan jobs.Job[greetArgs], 1)
	w, err := jobs.NewWorker(pool, quiet(), jobs.WithPollInterval(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	jobs.Register(w, func(_ context.Context, job jobs.Job[greetArgs]) error {
		got <- job
		return nil
	})
	if kinds := w.Kinds(); len(kinds) != 1 || kinds[0] != "greet" {
		t.Fatalf("Kinds() = %v", kinds)
	}
	startWorker(t, w)

	client, err := jobs.New(pool, quiet())
	if err != nil {
		t.Fatal(err)
	}
	id, err := client.Insert(ctx, greetArgs{Name: "ada"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case job := <-got:
		if job.ID != id || job.Args.Name != "ada" || job.Attempt != 1 {
			t.Fatalf("job = %+v, want ID %d, name ada, attempt 1", job, id)
		}
	case <-time.After(waitTimeout):
		t.Fatal("the worker did not run the job")
	}
}

func TestInsertTx(t *testing.T) {
	t.Parallel()
	pool := migrated(t)
	ctx := context.Background()
	client, err := jobs.New(pool, quiet())
	if err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.InsertTx(ctx, tx, greetArgs{Name: "gone"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, err := client.InsertTx(ctx, tx, greetArgs{Name: "kept"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	var name string
	if err := pool.QueryRow(ctx, `SELECT count(*), max(args->>'name') FROM river_job`).Scan(&n, &name); err != nil {
		t.Fatal(err)
	}
	if n != 1 || name != "kept" {
		t.Fatalf("river_job holds %d jobs (name %q), want only the committed job %d", n, name, id)
	}
	if _, err := client.InsertTx(ctx, nil, greetArgs{}); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("InsertTx(nil tx) err = %v", err)
	}
}

func TestInsertOptions(t *testing.T) {
	t.Parallel()
	pool := migrated(t)
	ctx := context.Background()
	client, err := jobs.New(pool, quiet())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	id, err := client.Insert(ctx, greetArgs{Name: "opts"},
		jobs.Queue("mail"), jobs.Priority(3), jobs.RunAt(at), jobs.MaxAttempts(4))
	if err != nil {
		t.Fatal(err)
	}
	var (
		queue       string
		priority    int
		maxAttempts int
		scheduled   time.Time
	)
	err = pool.QueryRow(ctx, `SELECT queue, priority, max_attempts, scheduled_at FROM river_job WHERE id = $1`, id).
		Scan(&queue, &priority, &maxAttempts, &scheduled)
	if err != nil {
		t.Fatal(err)
	}
	if queue != "mail" || priority != 3 || maxAttempts != 4 || !scheduled.Equal(at) {
		t.Fatalf("job row = queue %q, priority %d, max attempts %d, scheduled %s", queue, priority, maxAttempts, scheduled)
	}

	first, err := client.Insert(ctx, greetArgs{Name: "once"}, jobs.UniqueByArgs())
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Insert(ctx, greetArgs{Name: "once"}, jobs.UniqueByArgs())
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("unique inserts gave IDs %d and %d, want the same job", first, second)
	}
}

func TestInsertWithoutMigrations(t *testing.T) {
	t.Parallel()
	pool := jobsDB(t)
	client, err := jobs.New(pool, quiet())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Insert(context.Background(), greetArgs{})
	if err == nil || !strings.Contains(err.Error(), "Try: run the River migrations") {
		t.Fatalf("err = %v, want a hint to run the River migrations", err)
	}
}

func TestInsertPayloadLimit(t *testing.T) {
	t.Parallel()
	pool := migrated(t)
	ctx := t.Context()
	const limit = 128
	client, err := jobs.New(pool, quiet(), jobs.WithMaxPayloadBytes(limit))
	if err != nil {
		t.Fatal(err)
	}
	base, err := json.Marshal(greetArgs{})
	if err != nil {
		t.Fatal(err)
	}
	atLimit := greetArgs{Name: strings.Repeat("x", limit-len(base))}
	overLimit := greetArgs{Name: atLimit.Name + "x"}
	if _, err := client.Insert(ctx, overLimit); err == nil || !strings.Contains(err.Error(), "\nTry: ") {
		t.Fatalf("oversized insert error = %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := client.InsertTx(ctx, tx, overLimit); err == nil || !strings.Contains(err.Error(), "\nTry: ") {
		t.Fatalf("oversized transactional insert error = %v", err)
	}
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM river_job").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected inserts left %d rows: %v", count, err)
	}
	if _, err := client.Insert(ctx, atLimit); err != nil {
		t.Fatalf("insert at the limit: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM river_job").Scan(&count); err != nil || count != 1 {
		t.Fatalf("accepted insert left %d rows: %v", count, err)
	}
}

var payloadDecoded atomic.Bool

type payloadArgs struct {
	Data string `json:"data"`
}

func (payloadArgs) Kind() string { return "payload_limit_test" }

func (*payloadArgs) UnmarshalJSON([]byte) error {
	payloadDecoded.Store(true)
	return errors.New("unexpected argument decoding")
}

func TestWorkerPayloadLimitBeforeDecode(t *testing.T) {
	pool := migrated(t)
	ctx := t.Context()
	payloadDecoded.Store(false)
	client, err := jobs.New(pool, quiet(), jobs.WithMaxPayloadBytes(1024))
	if err != nil {
		t.Fatal(err)
	}
	id, err := client.Insert(ctx, payloadArgs{Data: strings.Repeat("x", 256)}, jobs.MaxAttempts(1))
	if err != nil {
		t.Fatal(err)
	}
	w, err := jobs.NewWorker(pool, quiet(), jobs.WithMaxPayloadBytes(128), jobs.WithPollInterval(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	jobs.Register(w, func(context.Context, jobs.Job[payloadArgs]) error {
		t.Error("oversized job reached the worker function")
		return nil
	})
	startWorker(t, w)
	deadline := time.NewTimer(waitTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		var state, failures string
		if err := pool.QueryRow(ctx, "SELECT state, coalesce(errors::text, '') FROM river_job WHERE id = $1", id).Scan(&state, &failures); err != nil {
			t.Fatal(err)
		}
		if state == "discarded" {
			if payloadDecoded.Load() || !strings.Contains(failures, "exceeding the 128-byte limit") {
				t.Fatalf("decoded = %v, failures = %s", payloadDecoded.Load(), failures)
			}
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("job did not fail: %s", state)
		case <-tick.C:
		}
	}
}

func TestPeriodic(t *testing.T) {
	t.Parallel()
	pool := migrated(t)
	var runs atomic.Int32
	done := make(chan struct{})
	w, err := jobs.NewWorker(pool, quiet(), jobs.WithPollInterval(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	jobs.Register(w, func(context.Context, jobs.Job[tickArgs]) error {
		if runs.Add(1) == 2 {
			close(done)
		}
		return nil
	})
	w.Periodic(300*time.Millisecond, tickArgs{})
	startWorker(t, w)
	select {
	case <-done:
	case <-time.After(waitTimeout):
		t.Fatalf("the periodic job ran %d times, want 2", runs.Load())
	}
}

func countTick(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM river_job WHERE kind = 'tick'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPeriodicOnStart(t *testing.T) {
	t.Parallel()
	pool := migrated(t)
	w, err := jobs.NewWorker(pool, quiet(), jobs.WithPollInterval(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	jobs.Register(w, func(context.Context, jobs.Job[tickArgs]) error { return nil })
	w.PeriodicOnStart(time.Hour, tickArgs{})
	startWorker(t, w)
	deadline := time.Now().Add(waitTimeout)
	for countTick(t, pool) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("PeriodicOnStart inserted %d jobs after Start, want 1", countTick(t, pool))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPeriodicNotOnStart(t *testing.T) {
	t.Parallel()
	pool := migrated(t)
	w, err := jobs.NewWorker(pool, quiet(), jobs.WithPollInterval(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	jobs.Register(w, func(context.Context, jobs.Job[tickArgs]) error { return nil })
	w.Periodic(time.Hour, tickArgs{})
	startWorker(t, w)
	time.Sleep(time.Second)
	if n := countTick(t, pool); n != 0 {
		t.Fatalf("Periodic inserted %d jobs right after Start, want 0", n)
	}
}

func TestGracefulStop(t *testing.T) {
	t.Parallel()
	pool := migrated(t)
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	var finished atomic.Bool
	w, err := jobs.NewWorker(pool, quiet(), jobs.WithPollInterval(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	jobs.Register(w, func(context.Context, jobs.Job[slowArgs]) error {
		close(started)
		<-release
		finished.Store(true)
		return nil
	})
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	client, err := jobs.New(pool, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Insert(ctx, slowArgs{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(waitTimeout):
		t.Fatal("the job did not start")
	}
	stopped := make(chan error, 1)
	go func() {
		stopCtx, cancel := context.WithTimeout(ctx, waitTimeout)
		defer cancel()
		stopped <- w.Stop(stopCtx)
	}()
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned (%v) before the running job finished", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if !finished.Load() {
		t.Fatal("Stop returned before the job finished")
	}
}

func TestStopTimeoutCancelsJobs(t *testing.T) {
	t.Parallel()
	pool := migrated(t)
	ctx := context.Background()
	started := make(chan struct{})
	w, err := jobs.NewWorker(pool, quiet(), jobs.WithPollInterval(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	jobs.Register(w, func(jobCtx context.Context, _ jobs.Job[slowArgs]) error {
		close(started)
		<-jobCtx.Done()
		return jobCtx.Err()
	})
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	client, err := jobs.New(pool, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Insert(ctx, slowArgs{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(waitTimeout):
		t.Fatal("the job did not start")
	}
	stopCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	err = w.Stop(stopCtx)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("Stop err = %v, want a deadline error with a Try line", err)
	}
}

func TestWorkerRules(t *testing.T) {
	t.Parallel()
	if _, err := jobs.New(nil); err == nil {
		t.Error("New(nil) must fail")
	}
	if _, err := jobs.NewWorker(nil); err == nil {
		t.Error("NewWorker(nil) must fail")
	}
	pool := &pgxpool.Pool{}
	if _, err := jobs.NewWorker(pool, jobs.WithQueue("mail", 0)); err == nil {
		t.Error("a queue with 0 workers must fail")
	}
	w, err := jobs.NewWorker(pool, quiet())
	if err != nil {
		t.Fatal(err)
	}
	// A worker with no job does nothing, so a new app's worker can run.
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start with no job: %v", err)
	}
	if err := w.Stop(context.Background()); err != nil {
		t.Fatalf("Stop with no job: %v", err)
	}
	if err := w.Start(context.Background()); err == nil {
		t.Error("a second Start must fail")
	}

	mustPanic := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s did not panic", name)
			}
		}()
		fn()
	}
	w2, err := jobs.NewWorker(pool, quiet())
	if err != nil {
		t.Fatal(err)
	}
	noop := func(context.Context, jobs.Job[greetArgs]) error { return nil }
	jobs.Register(w2, noop)
	mustPanic("a duplicate kind", func() { jobs.Register(w2, noop) })
	mustPanic("a zero interval", func() { w2.Periodic(0, greetArgs{}) })
	mustPanic("a zero start interval", func() { w2.PeriodicOnStart(0, greetArgs{}) })
	mustPanic("Register after Start", func() { jobs.Register(w, func(context.Context, jobs.Job[tickArgs]) error { return nil }) })
}
