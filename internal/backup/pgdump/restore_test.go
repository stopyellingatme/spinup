package pgdump

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/backup"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func expectTarget(r *remote.Fake) {
	r.Expect("sh").Return("exists\n/opt/my-app/backups/pgdump/"+label+".dump\t20\t1792065600\n", "", 0)
}

func expectDatabase(r *remote.Fake) {
	r.Expect("docker", "compose").Return("app-db\n", "", 0)
	r.Expect("docker", "compose").Return("app-user\n", "", 0)
}

func expectImage(r *remote.Fake) {
	r.Expect("docker", "compose").Return("postgres-id\n", "", 0)
	r.Expect("docker", "inspect", "--format", "{{.Image}}", "--", "postgres-id").Return("sha256:app-image\n", "", 0)
}

func TestPointInTime(t *testing.T) {
	t.Parallel()
	r := &remote.Fake{}
	p := newTest(t, r)
	for _, op := range []func() error{
		func() error { _, err := p.Verify(context.Background(), backup.Target{Time: time.Now()}); return err },
		func() error {
			return p.Restore(context.Background(), backup.Target{Time: time.Now()}, backup.Into{New: true})
		},
	} {
		if err := op(); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("point-in-time = %v", err)
		}
	}
	if len(r.Calls()) != 0 {
		t.Fatal("point-in-time started a remote command")
	}
}

func TestVerify(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"success", "create", "start", "ready", "restore", "query", "bad count", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			r := &remote.Fake{}
			p := newTest(t, r)
			expectTarget(r)
			expectDatabase(r)
			expectImage(r)
			stages := []string{"create", "start", "ready", "restore", "query"}
			for _, s := range stages {
				e := r.Expect()
				if s == stage {
					e.Fail(errors.New("test failure"))
					break
				}
				output := ""
				if s == "query" {
					output = "2\n"
					if stage == "bad count" {
						output = "invalid\n"
					}
				}
				e.Return(output, "", 0)
			}
			cleanup := r.Expect("sh", "-eu", "-c")
			if stage == "cleanup" {
				cleanup.Fail(errors.New("remove failed"))
			} else {
				cleanup.Return("", "", 0)
			}
			result, err := p.Verify(context.Background(), backup.Target{})
			if (err != nil) != (stage != "success") {
				t.Fatalf("Verify = %+v, %v", result, err)
			}
			if stage == "success" && (!result.OK || result.Label != label || result.RTO < 0) {
				t.Fatalf("result = %+v", result)
			}
			calls := r.Calls()
			create := calls[5].Argv
			if !strings.Contains(strings.Join(create, " "), "--network none") || strings.Contains(strings.Join(create, " "), "--publish") {
				t.Fatalf("verify network = %q", create)
			}
			if !strings.Contains(strings.Join(create, " "), "sha256:app-image") {
				t.Fatalf("image = %q", create)
			}
			last := calls[len(calls)-1].Argv
			if !strings.Contains(last[3], "docker rm -f -v") || !strings.Contains(last[5], "my-app-verify-"+label) {
				t.Fatalf("cleanup = %q", last)
			}
		})
	}
}

func TestRestoreNew(t *testing.T) {
	t.Parallel()
	r := &remote.Fake{}
	p := newTest(t, r)
	var output bytes.Buffer
	p.Env.Progress = &output
	p.Env.Environment.Host = "vm.example"
	p.Env.Environment.User = "deploy"
	p.Env.Environment.Port = 2222
	expectTarget(r)
	expectDatabase(r)
	expectImage(r)
	for range 4 {
		r.Expect().Return("", "", 0)
	}
	if err := p.Restore(context.Background(), backup.Target{Label: label}, backup.Into{New: true}); err != nil {
		t.Fatal(err)
	}
	calls := r.Calls()
	create := strings.Join(calls[5].Argv, " ")
	if !strings.Contains(create, "127.0.0.1:5433:5432") || !strings.Contains(create, "my-app-restore-"+label) {
		t.Fatalf("new restore = %s", create)
	}
	if len(calls) != 9 {
		t.Fatalf("new container was removed: %v", calls)
	}
	if !strings.Contains(output.String(), "-L 5433:127.0.0.1:5433") || !strings.Contains(output.String(), "deploy@vm.example") || !strings.Contains(output.String(), "2222") {
		t.Fatalf("instructions = %q", output.String())
	}
}

func TestRestoreInPlace(t *testing.T) {
	t.Parallel()
	r := &remote.Fake{}
	p := newTest(t, r)
	var output bytes.Buffer
	p.Env.Progress = &output
	expectTarget(r)
	expectDatabase(r)
	r.Expect("docker", "compose").Return("f\n", "", 0) // old database is absent
	r.Expect("docker", "compose").Return("", "", 0)    // createdb
	r.Expect("sh", "-eu", "-c").Return("", "", 0)      // streamed restore
	r.Expect("docker", "compose").Return("", "", 0)    // rename, via psql stdin
	if err := p.Restore(context.Background(), backup.Target{}, backup.Into{InPlace: true}); err != nil {
		t.Fatal(err)
	}
	calls := r.Calls()
	swap := calls[6]
	data := new(bytes.Buffer)
	if _, err := data.ReadFrom(swap.Stdin); err != nil {
		t.Fatal(err)
	}
	sql := data.String()
	for _, part := range []string{"ALLOW_CONNECTIONS false", "pg_terminate_backend", `RENAME TO :"old"`, `RENAME TO :"db"`} {
		if !strings.Contains(sql, part) {
			t.Fatalf("missing %q: %s", part, sql)
		}
	}
	if strings.Contains(strings.Join(swap.Argv, " "), " -c ") || strings.Contains(sql, "BEGIN") {
		t.Fatal("rename uses a transaction")
	}
	if !strings.Contains(output.String(), "dropdb") || !strings.Contains(output.String(), "app-db_old_"+label) {
		t.Fatalf("instructions = %q", output.String())
	}
}

func TestRestoreInvalidDestination(t *testing.T) {
	t.Parallel()
	for _, into := range []backup.Into{{}, {New: true, InPlace: true}} {
		r := &remote.Fake{}
		p := newTest(t, r)
		if err := p.Restore(context.Background(), backup.Target{}, into); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("Restore = %v", err)
		}
	}
}

// cancelRemote retains remote.Fake while recording the cleanup context.
type cancelRemote struct {
	*remote.Fake
	cancel          context.CancelFunc
	cleanupErr      error
	cleanupDeadline bool
}

func (r *cancelRemote) Run(ctx context.Context, c remote.Cmd) (runexec.Result, error) {
	if len(c.Argv) > 3 && c.Argv[3] == containerRestoreScript {
		r.cancel()
	}
	if len(c.Argv) > 3 && c.Argv[3] == cleanupScript {
		r.cleanupErr = ctx.Err()
		_, r.cleanupDeadline = ctx.Deadline()
	}
	return r.Fake.Run(ctx, c)
}

func TestVerifyCancellation(t *testing.T) {
	t.Parallel()
	r := &remote.Fake{}
	p := newTest(t, r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wrapped := &cancelRemote{Fake: r, cancel: cancel}
	p.Env.Remote = wrapped
	expectTarget(r)
	expectDatabase(r)
	expectImage(r)
	for range 3 {
		r.Expect().Return("", "", 0)
	}
	r.Expect("sh").Fail(context.Canceled)
	r.Expect("sh").Return("", "", 0)
	if _, err := p.Verify(ctx, backup.Target{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Verify = %v", err)
	}
	if wrapped.cleanupErr != nil || !wrapped.cleanupDeadline {
		t.Fatalf("cleanup context: %v, deadline=%v", wrapped.cleanupErr, wrapped.cleanupDeadline)
	}
}

func TestVerifyRTO(t *testing.T) {
	t.Parallel()
	r := &remote.Fake{}
	p := newTest(t, r)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	p.Now = func() time.Time { v := now; now = now.Add(3 * time.Second); return v }
	expectTarget(r)
	expectDatabase(r)
	expectImage(r)
	for range 4 {
		r.Expect().Return("", "", 0)
	}
	r.Expect("docker", "exec").Return("3\n", "", 0)
	r.Expect("sh").Return("", "", 0)
	result, err := p.Verify(context.Background(), backup.Target{})
	if err != nil {
		t.Fatal(err)
	}
	if result.RTO != 3*time.Second {
		t.Fatalf("RTO = %s", result.RTO)
	}
}

func TestRestoreNewFailureCleanup(t *testing.T) {
	t.Parallel()
	r := &remote.Fake{}
	p := newTest(t, r)
	expectTarget(r)
	expectDatabase(r)
	expectImage(r)
	for range 3 {
		r.Expect().Return("", "", 0)
	}
	r.Expect("sh").Fail(errors.New("pg_restore failed"))
	r.Expect("sh").Return("", "", 0)
	if err := p.Restore(context.Background(), backup.Target{}, backup.Into{New: true}); err == nil {
		t.Fatal("restore succeeded")
	}
}

func TestTargetErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, label, listing string }{{"empty", "", "missing\n"}, {"missing", "20261014T120000ZF", "exists\n"}, {"traversal", "../file", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &remote.Fake{}
			p := newTest(t, r)
			if tc.listing != "" {
				r.Expect("sh").Return(tc.listing, "", 0)
			}
			if _, err := p.Verify(context.Background(), backup.Target{Label: tc.label}); err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("target = %v", err)
			}
		})
	}
}

func TestInPlaceStopsBeforeSwap(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"create", "restore", "rename"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			r := &remote.Fake{}
			p := newTest(t, r)
			expectTarget(r)
			expectDatabase(r)
			r.Expect("docker", "compose").Return("f\n", "", 0)
			for _, s := range []string{"create", "restore", "rename"} {
				e := r.Expect()
				if s == stage {
					e.Fail(errors.New("database operation failed"))
					break
				}
				e.Return("", "", 0)
			}
			err := p.Restore(context.Background(), backup.Target{}, backup.Into{InPlace: true})
			if err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("in-place error = %v", err)
			}
		})
	}
}

func TestInPlaceOldDatabaseCollision(t *testing.T) {
	t.Parallel()
	r := &remote.Fake{}
	p := newTest(t, r)
	expectTarget(r)
	expectDatabase(r)
	r.Expect("docker", "compose").Return("t\n", "", 0)
	err := p.Restore(context.Background(), backup.Target{}, backup.Into{InPlace: true})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("collision = %v", err)
	}
	if len(r.Calls()) != 4 {
		t.Fatalf("database changed after collision: %q", r.Calls())
	}
}

func TestInPlaceDatabaseNames(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ db, maintenance string }{{"postgres", "template1"}, {`db'";--`, "postgres"}} {
		t.Run(tc.db, func(t *testing.T) {
			t.Parallel()
			r := &remote.Fake{}
			p := newTest(t, r)
			expectTarget(r)
			r.Expect("docker", "compose").Return(tc.db+"\n", "", 0)
			r.Expect("docker", "compose").Return("app-user\n", "", 0)
			r.Expect("docker", "compose").Return("f\n", "", 0)
			for range 3 {
				r.Expect().Return("", "", 0)
			}
			if err := p.Restore(context.Background(), backup.Target{}, backup.Into{InPlace: true}); err != nil {
				t.Fatal(err)
			}
			cmd := r.Calls()[6]
			argv := strings.Join(cmd.Argv, " ")
			if !strings.Contains(argv, "-d "+tc.maintenance) || !strings.Contains(argv, "db="+tc.db) {
				t.Fatalf("psql argv = %q", cmd.Argv)
			}
			var sql bytes.Buffer
			if _, err := sql.ReadFrom(cmd.Stdin); err != nil {
				t.Fatal(err)
			}
			if tc.db != "postgres" && strings.Contains(sql.String(), tc.db) {
				t.Fatal("database name interpolated into SQL")
			}
		})
	}
}
