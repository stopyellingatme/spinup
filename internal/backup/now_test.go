package backup

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestNow(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		kind Kind
		json bool
		want string
	}{
		{"default diff", "", false, "backup 20261015-082351F_20261015-082353D (diff) of staging: size 29.9 MiB, stored 408 B, took 1s\n"},
		{"full json", Full, true, `"label": "20261015-082351F_20261015-082353D"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := newProject(t, true, "", nil)
			f := &remote.Fake{}
			kind := tt.kind
			if kind == "" {
				kind = Diff
			}
			f.ExpectUpload("/opt/todo/backups/pgbackrest.conf")
			f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info_missing.json"), "", 0)
			f.Expect(Command("todo", "stanza-create")...).Return("", "", 0)
			f.Expect(Command("todo", "--type="+string(kind), "backup")...).Return("", "P00 INFO: backup command end\n", 0)
			f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
			expectMetrics(f)
			var out, errOut strings.Builder
			_, err := Now(context.Background(), Options{Dir: dir, Env: "staging", Kind: tt.kind, JSON: tt.json, Out: &out, ErrOut: &errOut, Remote: f})
			if err != nil {
				t.Fatal(err)
			}
			f.AssertDone(t)
			uploads := f.Uploads()
			if len(uploads) != 2 || !strings.Contains(string(uploads[1].Data), `backup_last_success_timestamp_seconds{kind="diff"} 1792052634`) || !strings.Contains(string(uploads[1].Data), `backup_last_size_bytes{kind="diff"} 31403056`) {
				t.Fatalf("backup metrics = %+v", uploads)
			}

			if !strings.Contains(out.String(), tt.want) {
				t.Fatalf("out = %q, want %q", out.String(), tt.want)
			}
			if !strings.Contains(errOut.String(), "backup command end") {
				t.Fatalf("progress = %q", errOut.String())
			}
			if f.CloseCalls() != 0 {
				t.Fatal("Now closed a runner it does not own")
			}
		})
	}
}

func TestListAndStatusCommands(t *testing.T) {
	t.Parallel()
	dir := newProject(t, true, "", nil)
	f := &remote.Fake{}
	for range 4 {
		f.ExpectUpload(RemoteConfigPath("todo"))
		f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
	}
	f.ExpectUpload(RemoteConfigPath("todo"))
	f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info_missing.json"), "", 0)
	f.ExpectUpload(RemoteConfigPath("todo"))
	f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info_missing.json"), "", 0)
	ctx := context.Background()
	run := func(json bool, fn func(Options) error) string {
		var out strings.Builder
		if err := fn(Options{Dir: dir, Env: "staging", JSON: json, Out: &out, Remote: f}); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	list := func(o Options) error { _, err := List(ctx, o); return err }
	status := func(o Options) error { _, err := ShowStatus(ctx, o); return err }

	if got := run(false, list); !strings.Contains(got, "20261015-082351F  full  2026-10-15T") || strings.Count(got, "\n") != 2 {
		t.Fatalf("list = %q", got)
	}
	var infos []Info
	if err := json.Unmarshal([]byte(run(true, list)), &infos); err != nil || len(infos) != 2 {
		t.Fatalf("list --json = %v, %v", infos, err)
	}
	if got := run(false, status); !strings.Contains(got, "stanza todo: ok (code 0)") || !strings.Contains(got, "last backup: 20261015-082351F_20261015-082353D (diff)") || !strings.Contains(got, "WAL archive: 000000010000000000000001 to 000000010000000000000005") {
		t.Fatalf("status = %q", got)
	}
	var s Status
	if err := json.Unmarshal([]byte(run(true, status)), &s); err != nil || !s.Exists || s.Last == nil {
		t.Fatalf("status --json = %+v, %v", s, err)
	}
	if got := run(false, list); got != "no backups in staging\n" {
		t.Fatalf("empty list = %q", got)
	}
	if got := run(false, status); !strings.Contains(got, "last backup: none") || !strings.Contains(got, "Try: run spinup backup now --env staging") {
		t.Fatalf("missing status = %q", got)
	}
	f.AssertDone(t)
}

func TestOptionsErrors(t *testing.T) {
	t.Parallel()
	withBackups := newProject(t, true, "", nil)
	without := newProject(t, false, "", nil)
	badRetention := newProject(t, true, "0", nil)
	ctx := context.Background()
	for _, tt := range []struct {
		name string
		o    Options
		want string
	}{
		{"no project", Options{Dir: t.TempDir(), Env: "staging"}, "find the spinup project"},
		{"no env", Options{Dir: withBackups}, "--env is missing"},
		{"unknown env", Options{Dir: withBackups, Env: "production"}, "select backup environment"},
		{"no backups module", Options{Dir: without, Env: "staging"}, "does not use the backups module"},
		{"bad retention", Options{Dir: badRetention, Env: "staging"}, "[backups] retention"},
		{"bad kind", Options{Dir: withBackups, Env: "staging", Kind: "weekly"}, "invalid backup type"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.o.Remote = &remote.Fake{}
			_, err := Now(ctx, tt.o)
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "Try") {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRetention(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int{"": 0, "4": 4, "12": 12, "999": 999} {
		c := &project.Config{}
		c.Backups.Retention = in
		if got, err := Retention(c); err != nil || got != want {
			t.Errorf("Retention(%q) = %d, %v", in, got, err)
		}
	}
	for _, in := range []string{"0", "-1", "1000", "4w"} {
		c := &project.Config{}
		c.Backups.Retention = in
		if _, err := Retention(c); err == nil {
			t.Errorf("Retention(%q) succeeded", in)
		}
	}
}
