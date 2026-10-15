package walg

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/backup"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func fixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/backup-list.json")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func backend(t *testing.T, r *remote.Fake) *WALG {
	t.Helper()
	b, err := New(backup.Env{App: "todo", Remote: r, Config: &project.Config{}})
	if err != nil {
		t.Fatal(err)
	}
	return b.(*WALG)
}

func TestListAndInfo(t *testing.T) {
	t.Parallel()
	r := &remote.Fake{}
	r.Expect("docker", "compose").Return(fixture(t), "", 0)
	b := backend(t, r)
	s, err := b.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Backups) != 2 || s.Last == nil || s.Last.Kind != backup.Incr || s.Last.Prior != "base_000000010000000000000001" || s.Last.Size != 2048 || s.Last.RepoSize != 1024 {
		t.Fatalf("status = %+v", s)
	}
	if s.Backups[0].Kind != backup.Full || s.Backups[0].Stop.Sub(s.Backups[0].Start).Seconds() != 10 {
		t.Fatalf("full = %+v", s.Backups[0])
	}
	args := strings.Join(r.Calls()[0].Argv, " ")
	for _, want := range []string{"compose.backups-walg.yml", "/run/secrets/WALG_*", "/run/secrets/AWS_*", "backup-list --json --detail"} {
		if !strings.Contains(args, want) {
			t.Errorf("command missing %q: %s", want, args)
		}
	}
	r.AssertDone(t)
}

func TestBackupAndExpire(t *testing.T) {
	t.Parallel()
	for _, kind := range []backup.Kind{backup.Full, backup.Diff, backup.Incr} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			r := &remote.Fake{}
			r.Expect("docker", "compose")
			r.Expect("docker", "compose").Return(fixture(t), "", 0)
			r.Expect("docker", "compose")
			b := backend(t, r)
			result, err := b.Backup(context.Background(), kind)
			if err != nil || result.Label != "base_000000010000000000000002_D_000000010000000000000001" {
				t.Fatalf("backup = %+v, %v", result, err)
			}
			if err = b.Expire(context.Background()); err != nil {
				t.Fatal(err)
			}
			calls := r.Calls()
			if !strings.Contains(strings.Join(calls[0].Argv, " "), `backup-push "$PGDATA"`) {
				t.Fatal(calls[0].Argv)
			}
			if forced := strings.Contains(strings.Join(calls[0].Argv, " "), "WALG_DELTA_MAX_STEPS=0"); forced != (kind == backup.Full) {
				t.Fatalf("kind %s: forced full = %v", kind, forced)
			}
			if !strings.Contains(strings.Join(calls[2].Argv, " "), "delete retain FULL 4 --confirm") {
				t.Fatal(calls[2].Argv)
			}
			r.AssertDone(t)
		})
	}
}

func TestInvalidListing(t *testing.T) {
	t.Parallel()
	for _, data := range []string{`{}`, `null`, `[{"backup_name":"../bad","time":"2026-10-15T01:00:00Z"}]`, `[{"backup_name":"base_000000010000000000000001","time":"bad"}]`} {
		t.Run(data, func(t *testing.T) {
			t.Parallel()
			r := &remote.Fake{}
			r.Expect().Return(data, "", 0)
			_, err := backend(t, r).List(context.Background())
			if err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("error = %v", err)
			}
			r.AssertDone(t)
		})
	}
}

func TestDeltaChain(t *testing.T) {
	t.Parallel()
	data := `[
 {"backup_name":"base_000000010000000000000001","time":"2026-10-15T00:00:00Z"},
 {"backup_name":"base_000000010000000000000002_D_000000010000000000000001","time":"2026-10-15T01:00:00Z"},
 {"backup_name":"base_000000010000000000000003_D_000000010000000000000002","time":"2026-10-15T02:00:00Z"}
 ]`
	entries, err := parseList([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if entries[2].Prior != entries[1].Label {
		t.Fatalf("prior = %s", entries[2].Prior)
	}
}

func TestNewValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, app, retention string
		remote               bool
		valid                bool
	}{
		{name: "defaults", app: "todo", remote: true, valid: true},
		{name: "retention", app: "todo", retention: "9", remote: true, valid: true},
		{name: "missing remote", app: "todo"},
		{name: "invalid app", app: "../todo", remote: true},
		{name: "zero", app: "todo", retention: "0", remote: true},
		{name: "negative", app: "todo", retention: "-1", remote: true},
		{name: "invalid retention", app: "todo", retention: "4;false", remote: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := backup.Env{App: tc.app, Config: &project.Config{}}
			e.Config.Backups.Retention = tc.retention
			if tc.remote {
				e.Remote = &remote.Fake{}
			}
			_, err := New(e)
			if (err == nil) != tc.valid {
				t.Fatalf("New = %v", err)
			}
			if err != nil && !strings.Contains(err.Error(), "Try:") {
				t.Fatal(err)
			}
		})
	}
}
