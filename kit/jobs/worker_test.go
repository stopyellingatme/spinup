package jobs_test

import (
	"context"
	"io"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

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
