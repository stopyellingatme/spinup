package pg

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/backup"
)

type testStrategy struct{ called bool }

func (*testStrategy) Plan(w io.Writer, cleanup bool)            { _, _ = io.WriteString(w, "link plan\n") }
func (s *testStrategy) Execute(context.Context, *Session) error { s.called = true; return nil }
func (s *testStrategy) Cleanup(context.Context, *Session) error { s.called = true; return nil }

func TestStrategyUsesSharedGuards(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		plan, know bool
		want       string
	}{
		{"plan", true, false, ""}, {"confirmation", false, false, "--i-know"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u, f, out := fixture(t, true)
			s := &testStrategy{}
			u.o.Plan, u.o.IKnow = tt.plan, tt.know
			err := RunStrategy(context.Background(), u.o, Deps{Remote: f}, s)
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("got %v", err)
			}
			if tt.want == "" && err != nil {
				t.Fatal(err)
			}
			if s.called || len(f.Calls()) != 0 {
				t.Fatal("guard called remote workflow")
			}
			if tt.plan && !strings.Contains(out.String(), "link plan") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestStrategyStanzaAndFullBackup(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"success", "stanza", "backup"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			u, f, _ := fixture(t, true)
			u.cfg.Backups.Backend = "pgbackrest"
			writePGFile(t, u.root, backup.ConfigPath, "[global]\nrepo1-retention-full=4\n[demo]\npg1-path=/var/lib/postgresql/data/pgdata\n")
			image, err := pinnedImage(18)
			if err != nil {
				t.Fatal(err)
			}
			expectUpdate(u, f)
			f.Expect(u.base("build", "postgres")...)
			f.ExpectUpload(backup.RemoteConfigPath("demo"))
			f.Expect(u.base("up", "-d", "--no-deps", "--no-build", "--force-recreate", "--wait", "--wait-timeout", "120", "postgres")...)
			e := f.Expect(u.base(backup.Command("demo", "stanza-upgrade")[4:]...)...)
			if stage == "stanza" {
				e.Fail(errors.New("bad stanza"))
			} else {
				f.ExpectUpload(backup.RemoteConfigPath("demo"))
				info := `[{"name":"demo","status":{"code":0,"message":"ok"},"backup":[{"label":"20261015-082351F","type":"full","timestamp":{"start":1792052631,"stop":1792052632},"info":{"size":512,"repository":{"delta":512}}}]}]`
				f.Expect(u.base(backup.Command("demo", "--output=json", "info")[4:]...)...).Return(info, "", 0)
				e = f.Expect(u.base(backup.Command("demo", "--type=full", "backup")[4:]...)...)
				if stage == "backup" {
					e.Fail(errors.New("backup failed"))
				} else {
					f.Expect(u.base(backup.Command("demo", "--output=json", "info")[4:]...)...).Return(info, "", 0)
					f.Expect("mkdir", "-p", "/opt/demo/backups/metrics")
					f.Expect("chmod", "0755")
					f.Expect("flock")
				}
			}
			err = (&Session{u}).Switch(context.Background(), image, "demo_postgres_data", "/var/lib/postgresql/18/docker")
			if stage == "success" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("failure ignored")
			}
			for _, upload := range f.Uploads() {
				if upload.Path == backup.RemoteConfigPath("demo") && !strings.Contains(string(upload.Data), "pg1-path=/var/lib/postgresql/18/docker") {
					t.Fatalf("old configuration uploaded: %s", upload.Data)
				}
			}
			f.AssertDone(t)
		})
	}
}
