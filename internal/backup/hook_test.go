package backup

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

const testConfig = "[global]\nrepo1-type=s3\nrepo1-retention-full=4\n"

// newProject writes a project with the backups module and the supplied migrations.
func newProject(t *testing.T, backups bool, retention string, migrations map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	toml := "modules = [\"deploy\", \"backups\"]\n\n[app]\nname = \"todo\"\nmodule = \"example.com/todo\"\n\n[env.staging]\nhost = \"203.0.113.10\"\nuser = \"deploy\"\n"
	if retention != "" {
		toml += "\n[backups]\nretention = \"" + retention + "\"\n"
	}
	files := map[string]string{"spinup.toml": toml}
	if backups {
		files[ConfigPath] = testConfig
	}
	for name, sql := range migrations {
		files[filepath.Join(MigrationsDir, name)] = sql
	}
	for name, data := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

var (
	psRunning = []string{"docker", "compose", "-p", "todo", "ps", "--status", "running", "-q", "postgres"}
	psql      = []string{"docker", "compose", "-p", "todo", "exec", "-T", "postgres", "psql", "-U", "app", "-d", "app", "-X", "-q", "-t", "-A", "-v", "ON_ERROR_STOP=1", "-c"}
)

func releaseHistory(f *remote.Fake, data string) {
	f.Expect("sh", "-c").Return(data, "", 0)
}

func TestHookRegistered(t *testing.T) {
	t.Parallel()
	for _, h := range deploy.PreDeployHooks() {
		if h.Name == HookName {
			return
		}
	}
	t.Fatal("the backup pre-deploy hook is not registered")
}

func TestHook(t *testing.T) {
	t.Parallel()
	safe := "-- +goose Up\n-- spinup:safe adds a nullable column\nALTER TABLE todos ADD COLUMN note text;\n"
	unsafe := "-- +goose Up\nALTER TABLE todos DROP COLUMN note;\n"
	gooseExists := func(f *remote.Fake, version string) {
		f.Expect(append(append([]string(nil), psql...), gooseTableQuery)...).Return("0\n", "", 0)
		f.Expect(append(append([]string(nil), psql...), gooseVersionQuery)...).Return(version+"\n", "", 0)
	}
	backupRun := func(f *remote.Fake) {
		f.ExpectUpload("/opt/todo/backups/pgbackrest.conf")
		f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
		f.Expect(Command("todo", "--type=diff", "backup")...).Return("", "", 0)
		f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
		expectMetrics(f)
	}
	for _, tt := range []struct {
		name       string
		backups    bool
		retention  string
		migrations map[string]string
		skip       bool
		script     func(f *remote.Fake)
		out, err   string
		upload     string
	}{
		{name: "no backups module", out: "does not use the backups module", script: func(*remote.Fake) {}},
		{name: "first deploy", backups: true, out: "first deploy", script: func(f *remote.Fake) {
			releaseHistory(f, "")
			f.Expect(psRunning...).Return("", "", 0)
		}},
		{name: "diff backup", backups: true, out: "backup: 20261015-082351F_20261015-082353D (diff)", script: func(f *remote.Fake) {
			releaseHistory(f, "[{}]")
			f.Expect(psRunning...).Return("4f2a\n", "", 0)
			backupRun(f)
		}},
		{name: "retention from spinup.toml", backups: true, retention: "8", out: "backup: 20261015", upload: "repo1-retention-full=8\n", script: func(f *remote.Fake) {
			releaseHistory(f, "[{}]")
			f.Expect(psRunning...).Return("4f2a\n", "", 0)
			backupRun(f)
		}},
		{name: "skip with safe migrations", backups: true, skip: true, out: "skipped (--skip-backup)",
			migrations: map[string]string{"00001_init.sql": unsafe, "00002_note.sql": safe},
			script: func(f *remote.Fake) {
				releaseHistory(f, "[{}]")
				f.Expect(psRunning...).Return("4f2a\n", "", 0)
				gooseExists(f, "1")
			}},
		{name: "skip refused for unsafe migration", backups: true, skip: true, err: "00003_drop.sql",
			migrations: map[string]string{"00001_init.sql": unsafe, "00002_note.sql": safe, "00003_drop.sql": unsafe},
			script: func(f *remote.Fake) {
				releaseHistory(f, "[{}]")
				f.Expect(psRunning...).Return("4f2a\n", "", 0)
				gooseExists(f, "1")
			}},
		{name: "skip refused on a database without goose", backups: true, skip: true, err: "00001_init.sql",
			migrations: map[string]string{"00001_init.sql": unsafe},
			script: func(f *remote.Fake) {
				releaseHistory(f, "[{}]")
				f.Expect(psRunning...).Return("4f2a\n", "", 0)
				f.Expect(append(append([]string(nil), psql...), gooseTableQuery)...).Return("-1\n", "", 0)
			}},
		{name: "skip refused when the check fails", backups: true, skip: true, err: "could not be checked",
			script: func(f *remote.Fake) {
				releaseHistory(f, "[{}]")
				f.Expect(psRunning...).Return("4f2a\n", "", 0)
				f.Expect(psql...).Return("", "psql: error\n", 2)
			}},
		{name: "backup failure stops the deploy", backups: true, err: "pre-deploy backup", script: func(f *remote.Fake) {
			releaseHistory(f, "[{}]")
			f.Expect(psRunning...).Return("4f2a\n", "", 0)
			f.ExpectUpload("/opt/todo/backups/pgbackrest.conf")
			f.Expect(Command("todo", "--output=json", "info")...).Return("", "P00 ERROR: [039]: HTTP request failed\n", 39)
		}},
		{name: "docker fails", backups: true, err: "list the running postgres container", script: func(f *remote.Fake) {
			releaseHistory(f, "[{}]")
			f.Expect(psRunning...).Return("", "Cannot connect to the Docker daemon\n", 1)
		}},
		{name: "stopped database after a release fails", backups: true, err: "start postgres", script: func(f *remote.Fake) {
			releaseHistory(f, "[{}]")
			f.Expect(psRunning...).Return("", "", 0)
		}},
		{name: "stopped database skips only safe migrations", backups: true, skip: true, out: "skipped (--skip-backup)", migrations: map[string]string{"00002_note.sql": safe}, script: func(f *remote.Fake) {
			releaseHistory(f, `[{"migration_version":"1"}]`)
			f.Expect(psRunning...).Return("", "", 0)
		}},
		{name: "stopped database refuses unsafe migrations", backups: true, skip: true, err: "00002_drop.sql", migrations: map[string]string{"00002_drop.sql": unsafe}, script: func(f *remote.Fake) {
			releaseHistory(f, `[{"migration_version":"1"}]`)
			f.Expect(psRunning...).Return("", "", 0)
		}},
		{name: "invalid retention", backups: true, retention: "weekly", err: "[backups] retention", script: func(*remote.Fake) {}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := newProject(t, tt.backups, tt.retention, tt.migrations)
			f := &remote.Fake{}
			tt.script(f)
			var out strings.Builder
			ctx := WithDeployOptions(context.Background(), DeployOptions{Dir: dir, SkipBackup: tt.skip})
			err := Hook(ctx, &deploy.HookContext{Env: project.Env{Name: "staging"}, App: "todo", Release: "20261015T000000Z-0123456789ab", Remote: f, Out: &out})
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) || !strings.Contains(err.Error(), "Try:") {
					t.Fatalf("err = %v, want %q and a Try line", err, tt.err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tt.out) {
				t.Fatalf("output %q does not contain %q", out.String(), tt.out)
			}
			f.AssertDone(t)
			if uploads := f.Uploads(); tt.upload != "" && (len(uploads) != 2 || !strings.Contains(string(uploads[0].Data), tt.upload)) {
				t.Fatalf("uploads = %+v, want %q", uploads, tt.upload)
			}
		})
	}
}

func TestHookThroughRunHooks(t *testing.T) {
	t.Parallel()
	dir := newProject(t, true, "", map[string]string{"00001_init.sql": "-- +goose Up\nDROP TABLE x;\n"})
	f := &remote.Fake{}
	releaseHistory(f, "[{}]")
	f.Expect(psRunning...).Return("4f2a\n", "", 0)
	f.Expect(append(append([]string(nil), psql...), gooseTableQuery)...).Return("-1\n", "", 0)
	ctx := WithDeployOptions(context.Background(), DeployOptions{Dir: dir, SkipBackup: true})
	err := deploy.RunHooks(ctx, []deploy.Hook{{Name: HookName, Run: Hook}}, &deploy.HookContext{App: "todo", Remote: f})
	if err == nil || !strings.Contains(err.Error(), `deploy hook "backup" failed`) {
		t.Fatalf("err = %v", err)
	}
	f.AssertDone(t)
	if err := Hook(context.Background(), nil); err == nil {
		t.Fatal("Hook accepted a nil context")
	}
}

func TestDeployOptions(t *testing.T) {
	t.Parallel()
	if got := DeployOptionsFrom(context.Background()); got != (DeployOptions{}) {
		t.Fatalf("empty context gives %+v", got)
	}
	want := DeployOptions{Dir: "/srv/app", SkipBackup: true}
	if got := DeployOptionsFrom(WithDeployOptions(context.Background(), want)); got != want {
		t.Fatalf("got %+v", got)
	}
}

func TestHasSafeMarker(t *testing.T) {
	t.Parallel()
	for sql, want := range map[string]bool{
		"-- spinup:safe\nALTER TABLE x ADD COLUMN y int;": true,
		"  -- spinup:safe adds a column\n":                true,
		"-- spinup:safe\t(reason)\n":                      true,
		"-- spinup:safely\n":                              false,
		"-- spinup:allow-destructive drop\n":              false,
		"SELECT 1; -- spinup:safe\n":                      false,
		"":                                                false,
	} {
		if got := HasSafeMarker([]byte(sql)); got != want {
			t.Errorf("HasSafeMarker(%q) = %v, want %v", sql, got, want)
		}
	}
}

func TestUnsafePending(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files := map[string]string{
		"00001_init.sql":   "DROP TABLE a;",
		"00002_safe.sql":   "-- spinup:safe\nCREATE TABLE b (id int);",
		"00010_drop.sql":   "DROP TABLE b;",
		"00003_alter.sql":  "ALTER TABLE a DROP COLUMN c;",
		"embed.go":         "package migrations",
		"notes_readme.sql": "-- no version",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := UnsafePending(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"00003_alter.sql", "00010_drop.sql"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("UnsafePending = %q, want %q", got, want)
	}
	if got, _ := UnsafePending(dir, 10); len(got) != 0 {
		t.Fatalf("all applied gives %q", got)
	}
	if _, err := UnsafePending(filepath.Join(dir, "missing"), 0); err == nil {
		t.Fatal("missing directory succeeded")
	}
}

func TestHookRecordsResults(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		migration string
		wantSafe  string
	}{
		{"safe pending migration", "-- +goose Up\n-- spinup:safe adds a nullable column\nALTER TABLE todos ADD COLUMN note text;\n", "true"},
		{"unsafe pending migration", "-- +goose Up\nALTER TABLE todos DROP COLUMN note;\n", "false"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := newProject(t, true, "", map[string]string{"00002_note.sql": tt.migration})
			f := &remote.Fake{}
			releaseHistory(f, "[{}]")
			f.Expect(psRunning...).Return("4f2a\n", "", 0)
			f.Expect(append(append([]string(nil), psql...), gooseTableQuery)...).Return("0\n", "", 0)
			f.Expect(append(append([]string(nil), psql...), gooseVersionQuery)...).Return("1\n", "", 0)
			f.ExpectUpload("/opt/todo/backups/pgbackrest.conf")
			f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
			f.Expect(Command("todo", "--type=diff", "backup")...).Return("", "", 0)
			f.Expect(Command("todo", "--output=json", "info")...).Return(fixture(t, "info.json"), "", 0)
			results := map[string]string{}
			ctx := WithDeployOptions(context.Background(), DeployOptions{Dir: dir})
			var out strings.Builder
			if err := Hook(ctx, &deploy.HookContext{App: "todo", Release: "r1", Remote: f, Out: &out, Results: results}); err != nil {
				t.Fatal(err)
			}
			f.AssertDone(t)
			if results[deploy.ResultMigrationsSafe] != tt.wantSafe {
				t.Fatalf("migrations_safe = %q, want %q", results[deploy.ResultMigrationsSafe], tt.wantSafe)
			}
			if _, err := time.Parse(time.RFC3339, results[deploy.ResultBackupTime]); err != nil {
				t.Fatalf("backup time %q: %v", results[deploy.ResultBackupTime], err)
			}
		})
	}
}
