package pgdump

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/backup"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

const label = "20261015T120000ZF"

func newTest(t *testing.T, r *remote.Fake) *PGDump {
	t.Helper()
	b, err := New(backup.Env{App: "my-app", Remote: r})
	if err != nil {
		t.Fatal(err)
	}
	p := b.(*PGDump)
	p.Now = func() time.Time { return time.Date(2026, 10, 15, 7, 0, 0, 0, time.FixedZone("CDT", -5*3600)) }
	t.Cleanup(func() { r.AssertDone(t) })
	return p
}

func TestBackup(t *testing.T) {
	t.Parallel()
	r := &remote.Fake{}
	p := newTest(t, r)
	r.Expect("sh", "-eu", "-c").Return("1234\n", "", 0)
	got, err := p.Backup(context.Background(), backup.Incr)
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != label || got.Kind != backup.Full || got.Size != 1234 || got.RepoSize != 1234 || got.Start.Location() != time.UTC {
		t.Fatalf("backup = %+v", got)
	}
	argv := r.Calls()[0].Argv
	script := argv[3]
	for _, want := range []string{"pg_dump -Fc", "printenv POSTGRES_DB", "printenv POSTGRES_USER", "--user postgres", "umask 077", `mv -- "$tmp" "$dest"`, `chmod 600 "$tmp"`, `stat -c %s`} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q: %s", want, script)
		}
	}
	if argv[5] != "my-app" || argv[6] != "/opt/my-app/backups/pgdump" || argv[7] != label {
		t.Fatalf("unsafe arguments: %q", argv)
	}
	if strings.Contains(script, "my-app") {
		t.Fatal("app interpolated into shell script")
	}
}

func TestList(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, output string
		count        int
		bad          bool
	}{
		{"unordered", "exists\n/opt/my-app/backups/pgdump/20261015T120000ZF.dump\t20\t1792065600\n/opt/my-app/backups/pgdump/20261014T120000ZF.dump\t10\t1791979200\n", 2, false},
		{"missing", "missing\n", 0, false}, {"empty", "exists\n", 0, false},
		{"bad size", "exists\n/opt/my-app/backups/pgdump/20261015T120000ZF.dump\tbad\t1\n", 0, true},
		{"bad label", "exists\n/tmp/../../bad.dump\t1\t1\n", 0, true},
		{"bad timestamp", "exists\n/opt/my-app/backups/pgdump/20261015T120000ZF.dump\t1\tbad\n", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &remote.Fake{}
			p := newTest(t, r)
			r.Expect("sh").Return(tc.output, "", 0)
			got, err := p.List(context.Background())
			if (err != nil) != tc.bad {
				t.Fatalf("list = %v, %v", got, err)
			}
			if tc.bad {
				if !strings.Contains(err.Error(), "Try:") {
					t.Fatal(err)
				}
				return
			}
			if len(got) != tc.count {
				t.Fatalf("list = %+v", got)
			}
			if tc.count == 2 && (got[0].Size != 10 || got[1].Label != label || !got[0].Stop.Before(got[1].Stop)) {
				t.Fatalf("list = %+v", got)
			}
		})
	}
}

func TestRetention(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		value string
		want  int
		bad   bool
	}{{"", 7, false}, {"1", 1, false}, {"2", 2, false}, {"0", 0, true}, {"-1", 0, true}, {"oops", 0, true}} {
		t.Run(tc.value, func(t *testing.T) {
			t.Parallel()
			cfg := &project.Config{}
			cfg.Backups.Retention = tc.value
			b, err := New(backup.Env{App: "my-app", Remote: &remote.Fake{}, Config: cfg})
			if (err != nil) != tc.bad {
				t.Fatalf("New = %v", err)
			}
			if !tc.bad && b.(*PGDump).Retention != tc.want {
				t.Fatalf("retention = %d", b.(*PGDump).Retention)
			}
		})
	}
}

func TestExpire(t *testing.T) {
	t.Parallel()
	for _, keep := range []int{1, 2, 7} {
		t.Run(string(rune('0'+keep)), func(t *testing.T) {
			t.Parallel()
			r := &remote.Fake{}
			p := newTest(t, r)
			p.Retention = keep
			r.Expect("sh").Return("exists\n/opt/my-app/backups/pgdump/20261013T120000ZF.dump\t1\t1\n/opt/my-app/backups/pgdump/20261014T120000ZF.dump\t1\t2\n/opt/my-app/backups/pgdump/20261015T120000ZF.dump\t1\t3\n", "", 0)
			if keep < 3 {
				r.Expect("rm", "-f", "--").Return("", "", 0)
			}
			if err := p.Expire(context.Background()); err != nil {
				t.Fatal(err)
			}
			if keep < 3 {
				args := r.Calls()[1].Argv
				if len(args) != 3+3-keep || strings.Contains(strings.Join(args, " "), label) {
					t.Fatalf("removed newest or incorrect count: %q", args)
				}
			}
		})
	}
}

func TestInfo(t *testing.T) {
	t.Parallel()
	for _, exists := range []bool{true, false} {
		t.Run(strconv.FormatBool(exists), func(t *testing.T) {
			t.Parallel()
			r := &remote.Fake{}
			p := newTest(t, r)
			if exists {
				expectTarget(r)
			} else {
				r.Expect("sh").Return("missing\n", "", 0)
			}
			r.Expect("docker", "compose").Return("pg_dump (PostgreSQL) 18.6\n", "", 0)
			info, err := p.Info(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if info.Stanza != "my-app" || info.Exists != exists || info.Version != "pg_dump (PostgreSQL) 18.6" {
				t.Fatalf("Info = %+v", info)
			}
			if exists && (info.Last == nil || info.Last.Label != label) {
				t.Fatalf("Last = %+v", info.Last)
			}
			if !exists && info.Last != nil {
				t.Fatalf("Last = %+v", info.Last)
			}
			args := r.Calls()[1].Argv
			if !slices.Contains(args, "/opt/my-app/current/compose.edge.yml") {
				t.Fatalf("incorrect base Compose file: %q", args)
			}
		})
	}
}

func TestFactoryValidation(t *testing.T) {
	t.Parallel()
	for _, app := range []string{"", "../app", "app;touch file", "-app", "APP", "app\n"} {
		if _, err := New(backup.Env{App: app, Remote: &remote.Fake{}}); err == nil {
			t.Errorf("accepted app %q", app)
		}
	}
	if _, err := New(backup.Env{App: "my-app"}); err == nil {
		t.Error("accepted nil runner")
	}
	if _, err := backup.NewBackend("pgdump", backup.Env{App: "my-app", Remote: &remote.Fake{}}); err != nil {
		t.Fatal(err)
	}
}

func TestBackupFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		output string
		fail   bool
	}{{"", true}, {"invalid", false}, {"-1", false}} {
		t.Run(tc.output, func(t *testing.T) {
			t.Parallel()
			r := &remote.Fake{}
			p := newTest(t, r)
			e := r.Expect("sh")
			if tc.fail {
				e.Fail(errors.New("pg_dump failed"))
			} else {
				e.Return(tc.output, "", 0)
			}
			if _, err := p.Backup(context.Background(), backup.Full); err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("Backup error = %v", err)
			}
		})
	}
}
