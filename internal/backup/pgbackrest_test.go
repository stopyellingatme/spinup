package backup

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/remote"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCommand(t *testing.T) {
	t.Parallel()
	got := Command("todo", "--type=diff", "backup")
	want := []string{"docker", "compose", "-p", "todo", "exec", "-T", "postgres", "sh", "-c",
		`for f in /run/secrets/PGBACKREST_*; do [ -f "$f" ] && export "${f##*/}=$(cat "$f")"; done; exec pgbackrest "$@"`,
		"pgbackrest", "--stanza=todo", "--type=diff", "backup"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Command =\n%q\nwant\n%q", got, want)
	}
	if RemoteConfigPath("todo") != "/opt/todo/backups/pgbackrest.conf" {
		t.Fatal(RemoteConfigPath("todo"))
	}
}

func TestParseKind(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		in   string
		want Kind
		err  bool
	}{{"", Diff, false}, {"full", Full, false}, {"diff", Diff, false}, {"incr", Incr, false}, {"FULL", "", true}, {"weekly", "", true}} {
		got, err := ParseKind(tt.in)
		if got != tt.want || (err != nil) != tt.err {
			t.Errorf("ParseKind(%q) = %q, %v", tt.in, got, err)
		}
		if err != nil && !strings.Contains(err.Error(), "Try:") {
			t.Errorf("ParseKind(%q) error has no Try line: %v", tt.in, err)
		}
	}
}

func TestParseInfo(t *testing.T) {
	t.Parallel()
	s, err := ParseInfo("todo", []byte(fixture(t, "info.json")))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Exists || s.Code != 0 || s.Message != "ok" || s.Cipher != "aes-256-cbc" || s.Version != "2.59.3" {
		t.Fatalf("status = %+v", s)
	}
	if s.WALMin != "000000010000000000000001" || s.WALMax != "000000010000000000000005" {
		t.Fatalf("WAL = %s to %s", s.WALMin, s.WALMax)
	}
	want := []Info{
		{Label: "20261015-082351F", Kind: Full, Start: time.Unix(1792052631, 0).UTC(), Stop: time.Unix(1792052632, 0).UTC(), Size: 31403056, RepoSize: 3813512},
		{Label: "20261015-082351F_20261015-082353D", Kind: Diff, Start: time.Unix(1792052633, 0).UTC(), Stop: time.Unix(1792052634, 0).UTC(), Size: 31403056, RepoSize: 408, Prior: "20261015-082351F"},
	}
	if !reflect.DeepEqual(s.Backups, want) {
		t.Fatalf("backups =\n%+v\nwant\n%+v", s.Backups, want)
	}
	if s.Last == nil || s.Last.Label != want[1].Label {
		t.Fatalf("last = %+v", s.Last)
	}

	for _, tt := range []struct{ name, stanza, data string }{
		{"missing stanza path", "todo", fixture(t, "info_missing.json")},
		{"other stanza", "other", fixture(t, "info.json")},
		{"empty list", "todo", "[]"},
	} {
		s, err := ParseInfo(tt.stanza, []byte(tt.data))
		if err != nil || s.Exists || s.Last != nil || len(s.Backups) != 0 {
			t.Errorf("%s: status = %+v, err = %v", tt.name, s, err)
		}
	}
	if _, err := ParseInfo("todo", []byte("P00 ERROR: no json")); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("invalid JSON error = %v", err)
	}
}

func TestBackup(t *testing.T) {
	t.Parallel()
	config := []byte("[global]\nrepo1-type=s3\nrepo1-retention-full=4\n")
	for _, tt := range []struct {
		name      string
		kind      Kind
		retention int
		script    func(f *remote.Fake)
		label     string
		err       string
	}{
		{
			name: "first run creates the stanza", kind: Full, retention: 6,
			script: func(f *remote.Fake) {
				f.ExpectUpload("/opt/todo/backups/pgbackrest.conf")
				f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info_missing.json"), "", 0)
				f.Expect(Command("todo", "stanza-create")...).Return("", "P00 INFO: stanza-create command end: completed successfully\n", 0)
				f.Expect(Command("todo", "--type=full", "backup")...).Return("", "P00 INFO: backup command end: completed successfully\n", 0)
				f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
			},
			label: "20261015-082351F_20261015-082353D",
		},
		{
			name: "existing stanza", kind: Diff,
			script: func(f *remote.Fake) {
				f.ExpectUpload("/opt/todo/backups/pgbackrest.conf")
				f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
				f.Expect(Command("todo", "--type=diff", "backup")...).Return("", "", 0)
				f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
			},
			label: "20261015-082351F_20261015-082353D",
		},
		{
			name: "backup fails", kind: Incr,
			script: func(f *remote.Fake) {
				f.ExpectUpload("/opt/todo/backups/pgbackrest.conf")
				f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
				f.Expect(Command("todo", "--type=incr", "backup")...).Return("", "P00 ERROR: [049]: unable to connect to S3\n", 49)
			},
			err: "run pgBackRest incr backup on the VM",
		},
		{
			name: "upload fails", kind: Diff,
			script: func(f *remote.Fake) {
				f.ExpectUpload("/opt/todo/backups/pgbackrest.conf").Fail(errors.New("permission denied"))
			},
			err: "spinup server bootstrap",
		},
		{
			name: "info fails", kind: Diff,
			script: func(f *remote.Fake) {
				f.ExpectUpload("/opt/todo/backups/pgbackrest.conf")
				f.Expect(Command("todo", "--output=json", "info")...).Return("", "service \"postgres\" is not running\n", 1)
			},
			err: "read pgBackRest info on the VM",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			tt.script(f)
			var progress strings.Builder
			b := &PGBackRest{App: "todo", Remote: f, Config: config, RetentionFull: tt.retention, Progress: &progress}
			result, err := b.Backup(context.Background(), tt.kind)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) || !strings.Contains(err.Error(), "Try:") {
					t.Fatalf("err = %v, want %q and a Try line", err, tt.err)
				}
				f.AssertDone(t)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			f.AssertDone(t)
			if result.Label != tt.label || result.Duration != time.Second || result.Size != 31403056 || result.RepoSize != 408 {
				t.Fatalf("result = %+v", result)
			}
			uploads := f.Uploads()
			if len(uploads) != 1 || string(uploads[0].Data) != string(WithRetention(config, tt.retention)) || uploads[0].Mode != 0o600 {
				t.Fatalf("uploads = %+v", uploads)
			}
		})
	}
}

func TestBackupRejectsEmptyKind(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	if _, err := (&PGBackRest{App: "todo", Remote: f}).Backup(context.Background(), ""); err == nil {
		t.Fatal("Backup accepted an empty kind")
	}
	f.AssertDone(t)
}

func TestPrepareWithoutConfigSkipsUpload(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
	created, err := (&PGBackRest{App: "todo", Remote: f}).Prepare(context.Background())
	if err != nil || created {
		t.Fatalf("Prepare = %v, %v", created, err)
	}
	f.AssertDone(t)
}

func TestListAndInfo(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
	f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info_missing.json"), "", 0)
	b := &PGBackRest{App: "todo", Remote: f}
	backups, err := b.List(context.Background())
	if err != nil || len(backups) != 2 {
		t.Fatalf("List = %v, %v", backups, err)
	}
	status, err := b.Info(context.Background())
	if err != nil || status.Exists || status.Message != "missing stanza path" {
		t.Fatalf("Info = %+v, %v", status, err)
	}
	f.AssertDone(t)
	if len(f.Uploads()) != 0 {
		t.Fatal("List or Info uploaded the configuration")
	}
}

func TestNotInM5(t *testing.T) {
	t.Parallel()
	b := &PGBackRest{App: "todo"}
	ctx := context.Background()
	_, verifyErr := b.Verify(ctx, Target{})
	for name, err := range map[string]error{"verify": verifyErr, "restore": b.Restore(ctx, Target{}, Into{New: true}), "expire": b.Expire(ctx)} {
		if !errors.Is(err, ErrNotImplemented) || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestMissingRemote(t *testing.T) {
	t.Parallel()
	if _, err := (&PGBackRest{App: "todo"}).Info(context.Background()); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("err = %v", err)
	}
	if _, err := (&PGBackRest{App: "todo", Config: []byte("x")}).Prepare(context.Background()); err == nil {
		t.Fatal("Prepare without a remote succeeded")
	}
}

func TestWithRetention(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, in string
		full     int
		want     string
	}{
		{"unchanged", "[global]\nrepo1-retention-full=4\n", 0, "[global]\nrepo1-retention-full=4\n"},
		{"replace", "[global]\nrepo1-type=s3\nrepo1-retention-full=4\n\n[todo]\n", 9, "[global]\nrepo1-type=s3\nrepo1-retention-full=9\n\n[todo]\n"},
		{"insert after global", "# c\n[global]\nrepo1-type=s3\n", 2, "# c\n[global]\nrepo1-retention-full=2\nrepo1-type=s3\n"},
		{"no global", "repo1-type=s3\n", 3, "[global]\nrepo1-retention-full=3\nrepo1-type=s3\n"},
	} {
		if got := string(WithRetention([]byte(tt.in), tt.full)); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	t.Parallel()
	for in, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 31403056: "29.9 MiB", 5 << 30: "5.0 GiB"} {
		if got := HumanSize(in); got != want {
			t.Errorf("HumanSize(%d) = %q, want %q", in, got, want)
		}
	}
}
