package jobs_test

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/stopyellingatme/spinup/kit/audit"
	"github.com/stopyellingatme/spinup/kit/db"
	"github.com/stopyellingatme/spinup/kit/jobs"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

type auditedArgs struct{}

func (auditedArgs) Kind() string { return "audited_write" }

// TestJobAuditActor proves ADR 0025 decision 4.9: the job context holds the
// system actor, and a write through audit.InTx in the job records it.
func TestJobAuditActor(t *testing.T) {
	t.Parallel()
	pool := migrated(t)
	ctx := context.Background()
	sql, err := os.ReadFile("../../internal/templates/packs/audit/files/api/db/kit/00006_audit_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	m, err := db.NewMigrator(testkit.URL(t, pool), fstest.MapFS{
		"00006_audit_events.sql": {Data: sql},
		"00007_notes.sql": {Data: []byte(`-- +goose Up
CREATE TABLE notes (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, body text NOT NULL);
CREATE TRIGGER notes_audit AFTER INSERT OR UPDATE OR DELETE ON notes
    FOR EACH ROW EXECUTE FUNCTION audit_row_change('', 'id');

-- +goose Down
DROP TABLE notes;
`)},
	}, db.WithOutput(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()

	type result struct {
		actor audit.Actor
		ok    bool
		err   error
	}
	done := make(chan result, 1)
	w, err := jobs.NewWorker(pool, quiet(), jobs.WithPollInterval(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	jobs.Register(w, func(ctx context.Context, _ jobs.Job[auditedArgs]) error {
		actor, ok := audit.ActorFrom(ctx)
		err := audit.InTx(ctx, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO notes (body) VALUES ('from a job')`)
			return err
		})
		done <- result{actor, ok, err}
		return err
	})
	startWorker(t, w)
	client, err := jobs.New(pool, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Insert(ctx, auditedArgs{}); err != nil {
		t.Fatal(err)
	}
	var r result
	select {
	case r = <-done:
	case <-time.After(waitTimeout):
		t.Fatal("the worker did not run the job")
	}
	if r.err != nil || !r.ok || r.actor != audit.System(audit.SourceJob, "audited_write") {
		t.Fatalf("job actor = %+v, %v, %v", r.actor, r.ok, r.err)
	}
	var kind, source, reason string
	if err := pool.QueryRow(ctx, `SELECT actor_kind, source, reason FROM audit_events WHERE table_name = 'notes'`).Scan(&kind, &source, &reason); err != nil {
		t.Fatal(err)
	}
	if kind != "system" || source != "job" || reason != "audited_write" {
		t.Errorf("event actor = %s %s %s, want system job audited_write", kind, source, reason)
	}
}

type limitedArgs struct{}

func (limitedArgs) Kind() string { return "limited" }

type longArgs struct{}

func (longArgs) Kind() string { return "long" }

// Timeout overrides the worker default for this kind.
func (longArgs) Timeout() time.Duration { return -1 }

type shortArgs struct{}

func (shortArgs) Kind() string { return "short" }

func (shortArgs) Timeout() time.Duration { return 200 * time.Millisecond }

type cancelArgs struct{}

func (cancelArgs) Kind() string { return "cancel_me" }

func TestJobTimeout(t *testing.T) {
	t.Parallel()
	pool := migrated(t)
	ctx := context.Background()
	type outcome struct {
		kind    string
		elapsed time.Duration
		err     error
	}
	got := make(chan outcome, 4)
	// wait blocks until ctx ends or limit passes, and reports ctx's error.
	wait := func(kind string, limit time.Duration) func(context.Context) {
		return func(c context.Context) {
			start := time.Now()
			var err error
			select {
			case <-c.Done():
				err = c.Err()
			case <-time.After(limit):
			}
			got <- outcome{kind, time.Since(start), err}
		}
	}
	w, err := jobs.NewWorker(pool, quiet(), jobs.WithPollInterval(50*time.Millisecond), jobs.WithJobTimeout(300*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	limited, long, short := wait("limited", 5*time.Second), wait("long", 1500*time.Millisecond), wait("short", 5*time.Second)
	jobs.Register(w, func(c context.Context, _ jobs.Job[limitedArgs]) error { limited(c); return nil })
	jobs.Register(w, func(c context.Context, _ jobs.Job[longArgs]) error { long(c); return nil })
	jobs.Register(w, func(c context.Context, _ jobs.Job[shortArgs]) error { short(c); return nil })
	startWorker(t, w)
	client, err := jobs.New(pool, quiet())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []jobs.Args{limitedArgs{}, longArgs{}, shortArgs{}} {
		if _, err := client.Insert(ctx, a, jobs.MaxAttempts(1)); err != nil {
			t.Fatal(err)
		}
	}
	res := map[string]outcome{}
	for range 3 {
		select {
		case o := <-got:
			res[o.kind] = o
		case <-time.After(waitTimeout):
			t.Fatal("a job did not finish")
		}
	}
	if o := res["limited"]; !errors.Is(o.err, context.DeadlineExceeded) || o.elapsed > 3*time.Second {
		t.Errorf("default limit: err=%v after %s, want a deadline error near 300ms", o.err, o.elapsed)
	}
	if o := res["long"]; o.err != nil {
		t.Errorf("Timeouter -1: err=%v after %s, want no cancel (the default limit is 300ms)", o.err, o.elapsed)
	}
	if o := res["short"]; !errors.Is(o.err, context.DeadlineExceeded) || o.elapsed > 3*time.Second {
		t.Errorf("Timeouter 200ms: err=%v after %s, want a deadline error", o.err, o.elapsed)
	}
}

func TestCancelStopsRetries(t *testing.T) {
	t.Parallel()
	pool := migrated(t)
	ctx := context.Background()
	cause := errors.New("malformed filing")
	var attempts atomic.Int32
	done := make(chan struct{}, 4)
	w, err := jobs.NewWorker(pool, quiet(), jobs.WithPollInterval(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	jobs.Register(w, func(context.Context, jobs.Job[cancelArgs]) error {
		attempts.Add(1)
		defer func() { done <- struct{}{} }()
		return jobs.Cancel(cause)
	})
	startWorker(t, w)
	client, err := jobs.New(pool, quiet())
	if err != nil {
		t.Fatal(err)
	}
	id, err := client.Insert(ctx, cancelArgs{}, jobs.MaxAttempts(5))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(waitTimeout):
		t.Fatal("the job did not run")
	}
	var state string
	var attempt int
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(ctx, `SELECT state, attempt FROM river_job WHERE id = $1`, id).Scan(&state, &attempt); err != nil {
			t.Fatal(err)
		}
		if state == "cancelled" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if state != "cancelled" || attempt != 1 || attempts.Load() != 1 {
		t.Fatalf("state=%s attempt=%d runs=%d, want cancelled after 1 attempt", state, attempt, attempts.Load())
	}
}

func TestCancelErrors(t *testing.T) {
	t.Parallel()
	cause := &os.PathError{Op: "open", Path: "x.xml", Err: os.ErrNotExist}
	err := jobs.Cancel(cause)
	if !errors.Is(err, cause) || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("errors.Is lost the cause: %v", err)
	}
	var pe *os.PathError
	if !errors.As(err, &pe) || pe != cause {
		t.Errorf("errors.As lost the cause: %v", err)
	}
	var ce *river.JobCancelError
	if !errors.As(err, &ce) {
		t.Errorf("Cancel must return a River cancel error: %T", err)
	}
	if msg := jobs.Cancel(nil).Error(); !strings.Contains(msg, "cancelled") || strings.Contains(msg, "nil") {
		t.Errorf("Cancel(nil) message = %q", msg)
	}
}
