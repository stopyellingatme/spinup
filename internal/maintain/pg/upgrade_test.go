package pg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/ui"
	"go.yaml.in/yaml/v3"
)

const releaseID = "20261015T062420Z-abcdef123456"
const edgeYAML = `services:
  postgres:
    image: demo-postgres:17.10
    build:
      context: .
      dockerfile: Dockerfile.postgres
    environment:
      POSTGRES_USER: app
      POSTGRES_DB: app
      PGDATA: /var/lib/postgresql/data/pgdata
    volumes:
      - postgres_data:/var/lib/postgresql/data
      - /opt/demo/secrets:/run/secrets:ro
volumes:
  postgres_data:
`
const counts = "1|0|t\n2|20261001000000|t\n[\"public.goose_db_version\", 2]\n[\"public.users\", 42]\n"

func projectDir(t *testing.T, window string, protected bool) string {
	t.Helper()
	dir := t.TempDir()
	config := `modules = ["api", "deploy", "backups"]
[app]
name = "demo"
module = "example.com/demo"
[backups]
backend = "pgdump"
[maintenance]
window = ` + strconv.Quote(window) + `
[env.production]
host = "vm.example.com"
user = "deploy"
public_url = "https://example.com"
protected = ` + strconv.FormatBool(protected) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "spinup.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "deploy", "environments"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deploy", "environments", "production.toml"), []byte("smoke_urls = [\"/ready\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}
func fixture(t *testing.T, major bool) (*upgrade, *remote.Fake, *bytes.Buffer) {
	t.Helper()
	root := projectDir(t, "", false)
	cfg, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	env, err := cfg.ResolveEnv("production")
	if err != nil {
		t.Fatal(err)
	}
	f := &remote.Fake{}
	out := &bytes.Buffer{}
	u := &upgrade{o: Options{Dir: root, Env: "production", Major: major, To: 18, IKnow: true, Out: out, ErrOut: out}, root: root, cfg: cfg, env: env, vm: f, dir: "/opt/demo/releases/" + releaseID, current: version{17, 10}, rel: deploy.Release{Release: releaseID, Color: deploy.Blue, GitSHA: "abcdef123456abcdef"}}
	u.files = []string{u.dir + "/compose.edge.yml", u.dir + "/compose.backups.yml"}
	u.live.ID = "old-pg"
	u.live.Image = "sha256:old-image"
	u.live.Config.Image = "demo-postgres:17.10"
	u.live.Config.User = "1000:1000"
	u.live.Config.Env = []string{"POSTGRES_USER=app", "POSTGRES_DB=app"}
	u.live.Config.Labels = map[string]string{"com.docker.compose.project.config_files": strings.Join(u.files, ",")}
	if err = json.Unmarshal([]byte(`[{"Type":"volume","Name":"demo_postgres_data","Destination":"/var/lib/postgresql/data"}]`), &u.live.Mounts); err != nil {
		t.Fatal(err)
	}
	return u, f, out
}
func expectBackup(f *remote.Fake) {
	f.Expect("sh", "-eu", "-c").Return("512\n", "", 0)
	f.Expect("sh", "-ec").Return("", "", 0)
	f.Expect("mkdir", "-p", "/opt/demo/backups/metrics")
	f.Expect("chmod", "0755", "/opt/demo/backups/metrics")
	f.ExpectUpload("/opt/demo/backups/metrics/backup.prom")
}
func expectRecovery(u *upgrade, f *remote.Fake) {
	if u.o.Major {
		f.Expect("mkdir", "-m", "0700", u.operationDir())
	} else {
		f.Expect("mkdir", "-m", "0700", "-p", u.operationDir())
	}
	for _, file := range append(append([]string{}, u.files...), u.dir+"/Dockerfile.postgres", u.dir+"/.env") {
		f.Expect("cp", "--", file, u.operationDir()+"/"+filepath.Base(file))
	}
	f.ExpectUpload(u.operationDir() + "/rollback.sh")
	f.Expect("docker", "create", "--name", "demo-pg-recovery-17-10")
	if u.o.Major {
		f.ExpectUpload(u.operationDir() + "/state.json")
	}
}
func expectUpdate(u *upgrade, f *remote.Fake) {
	f.Expect("cat", u.dir+"/compose.edge.yml").Return(edgeYAML, "", 0)
	f.Expect("cat", u.dir+"/Dockerfile.postgres").Return("FROM postgres:17.10\nRUN echo pgbackrest\n", "", 0)
	f.ExpectUpload(u.dir + "/Dockerfile.postgres")
	f.ExpectUpload(u.dir + "/compose.edge.yml")
}
func expectInspect(u *upgrade, f *remote.Fake, v string) {
	rel, _ := json.Marshal(u.rel)
	f.Expect("cat", "/opt/demo/current/release.json").Return(string(rel), "", 0)
	f.Expect("docker", "ps", "-q").Return("old-pg\n", "", 0)
	data, _ := json.Marshal(u.live)
	f.Expect("docker", "inspect", "--format", "{{json .}}", "old-pg").Return(string(data), "", 0)
	f.Expect("docker", "exec", "-i", "old-pg").Return(v, "", 0)
}
func TestMinorOrderAndNoOp(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		current version
		want    string
	}{{"upgrade", version{17, 10}, "now runs 17.11"}, {"equal", version{17, 11}, "already matches"}, {"newer", version{17, 12}, "Refusing a downgrade"}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u, f, out := fixture(t, false)
			u.current = tt.current
			if tt.name == "upgrade" {
				expectBackup(f)
				expectRecovery(u, f)
				expectUpdate(u, f)
				f.Expect(u.base("build", "postgres")...)
				f.Expect(u.base("up", "-d", "--no-deps", "--no-build", "--force-recreate", "postgres")...)
				f.Expect(u.base("up", "-d", "--no-deps", "--no-build", "--wait", "--wait-timeout", "120", "postgres")...)
				f.Expect(u.base("ps", "-q", "postgres")...).Return("new-pg\n", "", 0)
				f.Expect("docker", "exec", "-i", "new-pg").Return("17.11 (Debian)\n", "", 0)
			}
			err := u.minor(context.Background())
			text := out.String()
			if err != nil {
				text += err.Error()
			}
			if !strings.Contains(text, tt.want) {
				t.Fatalf("want %q in %s", tt.want, text)
			}
			if tt.name != "newer" && err != nil {
				t.Fatal(err)
			}
			f.AssertDone(t)
			if tt.name == "upgrade" {
				assertRecovery(t, u, f)
				assertCompose(t, u, f, false)
			}
		})
	}
}
func expectMajorUntilVerify(u *upgrade, f *remote.Fake, restored string) {
	expectRecovery(u, f)
	f.Expect("sh", "-ec", stopColorsScript("demo"))
	expectBackup(f)
	f.Expect("docker", "exec", "-i", "old-pg").Return(counts, "", 0)
	f.Expect("sh", "-ec").Return("demo_pgdata_18", "", 0)
	f.Expect("docker", "run", "-d", "--name", "demo-pg-upgrade-18", "--network", "none")
	f.Expect("docker", "exec", "demo-pg-upgrade-18", "sh", "-ec", readyScript)
	f.Expect("sh", "-ec")
	f.Expect("docker", "exec", "-i", "demo-pg-upgrade-18").Return("ANALYZE\n", "", 0)
	f.Expect("docker", "exec", "-i", "demo-pg-upgrade-18").Return(restored, "", 0)
}
func TestMajorOrderAndMismatch(t *testing.T) {
	t.Parallel()
	for _, mismatch := range []bool{false, true} {
		t.Run(strconv.FormatBool(mismatch), func(t *testing.T) {
			t.Parallel()
			u, f, out := fixture(t, true)
			// Major upgrades must use pgdump even when the configured backend is pgBackRest.
			u.cfg.Backups.Backend = "pgbackrest"
			report := counts
			if mismatch {
				report = strings.Replace(counts, "42", "41", 1)
			}
			expectMajorUntilVerify(u, f, report)
			var urls []string
			if !mismatch {
				f.Expect("docker", "stop", "demo-pg-upgrade-18")
				f.Expect(u.base("stop", "postgres")...)
				expectUpdate(u, f)
				f.Expect(u.base("build", "postgres")...)
				f.Expect(u.base("up", "-d", "--no-deps", "--no-build", "--force-recreate", "--wait", "--wait-timeout", "120", "postgres")...)
				f.Expect(u.color("--profile", "migrate", "run", "--rm", "-T", "migrate")...)
				f.Expect(u.color("up", "-d", "--no-deps", "--wait", "api")...)
				u.d.HTTPGet = func(_ context.Context, url string) (int, []byte, error) {
					urls = append(urls, url)
					return 200, []byte(`{"version":"abcdef123456"}`), nil
				}
				f.ExpectUpload(u.operationDir() + "/state.json")
			}
			f.Expect("docker", "rm", "-f", "demo-pg-upgrade-18")
			err := u.major(context.Background())
			if mismatch {
				if err == nil || !strings.Contains(err.Error(), "counts differ") {
					t.Fatalf("got %v", err)
				}
				for _, c := range f.Calls() {
					if slices.Contains(c.Argv, "stop") && slices.Contains(c.Argv, "postgres") {
						t.Fatal("swap started on mismatch")
					}
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(urls, []string{"https://example.com/healthz", "https://example.com/ready", "https://example.com/version"}) {
					t.Fatal(urls)
				}
				assertCompose(t, u, f, true)
				if !strings.Contains(out.String(), "answers.db_version") || !strings.Contains(out.String(), "only after these repository changes") {
					t.Fatal(out.String())
				}
			}
			assertRecovery(t, u, f)
			f.AssertDone(t)
		})
	}
}
func assertRecovery(t *testing.T, u *upgrade, f *remote.Fake) {
	t.Helper()
	for _, upload := range f.Uploads() {
		if strings.HasSuffix(upload.Path, "/rollback.sh") {
			script := string(upload.Data)
			for _, want := range []string{"sha256:old-image", "demo-postgres:17.10", "--force-recreate", "--pull", "never", u.dir + "/.env", u.dir + "/compose.backups.yml"} {
				if !strings.Contains(script, want) {
					t.Errorf("recovery lacks %q: %s", want, script)
				}
			}
			if upload.Mode != 0700 {
				t.Fatal(upload.Mode)
			}
			if !strings.Contains(u.recovery, "vm.example.com") {
				t.Fatal(u.recovery)
			}
			return
		}
	}
	t.Fatal("no recovery script")
}
func assertCompose(t *testing.T, u *upgrade, f *remote.Fake, major bool) {
	t.Helper()
	for _, upload := range f.Uploads() {
		if upload.Path == u.dir+"/compose.edge.yml" {
			var doc map[string]any
			if err := yaml.Unmarshal(upload.Data, &doc); err != nil {
				t.Fatal(err)
			}
			service := doc["services"].(map[string]any)["postgres"].(map[string]any)
			want := "17.11"
			if major {
				want = "18.6-trixie"
			}
			if !strings.Contains(service["image"].(string), want) {
				t.Fatal(service)
			}
			if major {
				vol := doc["volumes"].(map[string]any)["postgres_data"].(map[string]any)
				if vol["external"] != true || vol["name"] != "demo_pgdata_18" {
					t.Fatal(vol)
				}
				if service["environment"].(map[string]any)["PGDATA"] != "/var/lib/postgresql/18/docker" {
					t.Fatal(service)
				}
			}
			return
		}
	}
	t.Fatal("no Compose update")
}

func TestRefusalsAndPlan(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name      string
		o         Options
		window    string
		protected bool
		want      string
	}{
		{name: "outside window", window: "Sun 02:00-05:00", want: "outside the maintenance window"},
		{name: "protected", protected: true, want: "protected environment"},
		{name: "major confirmation", o: Options{Major: true, To: 18}, want: "--i-know"},
		{name: "unsupported", o: Options{Major: true, To: 19}, want: "no supported pin"},
		{name: "cleanup confirmation", o: Options{Major: true, Cleanup: true}, want: "--i-know"},
		{name: "cleanup target", o: Options{Major: true, Cleanup: true, To: 18}, want: "without --to"},
		{name: "minor plan", o: Options{Plan: true}, window: "Sun 02:00-05:00", protected: true, want: "Plan:"},
		{name: "major plan", o: Options{Major: true, To: 18, Plan: true}, want: "Plan:"},
		{name: "cleanup plan", o: Options{Major: true, Cleanup: true, Plan: true}, want: "Plan:"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			out := &bytes.Buffer{}
			o := tt.o
			o.Dir = projectDir(t, tt.window, tt.protected)
			o.Env = "production"
			o.Out = out
			err := Run(context.Background(), o, Deps{Remote: f, Now: func() time.Time { return now }})
			text := out.String()
			if err != nil {
				text += err.Error()
			}
			if !strings.Contains(text, tt.want) {
				t.Fatalf("got %q, %v", text, err)
			}
			if len(f.Calls()) != 0 || len(f.Uploads()) != 0 {
				t.Fatal("guard or plan changed the VM")
			}
		})
	}
}
func TestLockHeld(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	f := &remote.Fake{}
	f.Expect("mkdir", "/opt/demo/deploy.lock").Return("", "held", 1)
	info, _ := json.Marshal(deploy.LockInfo{Owner: "other", Acquired: now, TTLSeconds: 900})
	f.Expect("sh", "-c").Return(string(info), "", 0)
	err := Run(context.Background(), Options{Dir: projectDir(t, "", false), Env: "production"}, Deps{Remote: f, Now: func() time.Time { return now }})
	var uiErr *ui.Error
	if !errors.As(err, &uiErr) || uiErr.Code != ui.ExitLocked {
		t.Fatalf("want exit 5, got %v", err)
	}
	f.AssertDone(t)
}
func TestMajorVersionRefusals(t *testing.T) {
	t.Parallel()
	for _, to := range []int{16, 17, 19, 15} {
		t.Run(strconv.Itoa(to), func(t *testing.T) {
			t.Parallel()
			u, f, _ := fixture(t, true)
			u.o.To = to
			if err := u.major(context.Background()); err == nil {
				t.Fatal("upgrade accepted")
			}
			if len(f.Calls()) > 0 {
				t.Fatal("changed VM")
			}
		})
	}
}
func TestCleanup(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name             string
		complete, active bool
		wantError        bool
	}{{"complete", true, true, false}, {"incomplete", false, true, true}, {"wrong active volume", true, false, true}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u, f, _ := fixture(t, true)
			u.o.Cleanup = true
			u.o.To = 0
			state := upgradeState{OldVolume: "demo_postgres_data", NewVolume: "demo_pgdata_18", Anchor: "demo-pg-recovery-17-10", Complete: tt.complete}
			data, _ := json.Marshal(state)
			f.Expect("cat", u.operationDir()+"/state.json").Return(string(data), "", 0)
			if tt.complete {
				if tt.active {
					u.live.Mounts[0].Name = "demo_pgdata_18"
				}
				expectInspect(u, f, "18.6\n")
			}
			if !tt.wantError {
				f.Expect("docker", "rm", state.Anchor)
				f.Expect("docker", "volume", "rm", "--", state.OldVolume)
				f.Expect("rm", "-rf", "--", u.operationDir())
			}
			err := u.cleanup(context.Background())
			if (err != nil) != tt.wantError {
				t.Fatalf("got %v", err)
			}
			f.AssertDone(t)
		})
	}
}
func TestRunFailurePrintsRecovery(t *testing.T) {
	t.Parallel()
	u, f, out := fixture(t, false)
	f.Expect("mkdir", "/opt/demo/deploy.lock")
	f.ExpectUpload("/opt/demo/deploy.lock/owner.json")
	expectInspect(u, f, "17.10\n")
	expectBackup(f)
	expectRecovery(u, f)
	expectUpdate(u, f)
	f.Expect(u.base("build", "postgres")...).Fail(errors.New("build failed"))
	f.Expect("sh", "-c") // Release the deploy lock even on failure.
	err := Run(context.Background(), u.o, Deps{Remote: f})
	if err == nil || !strings.Contains(err.Error(), "rollback.sh") || !strings.Contains(out.String(), "Return to the old state on the VM:") {
		t.Fatalf("%v\n%s", err, out)
	}
	f.AssertDone(t)
}
func TestSnapshotIncludesAllTablesAndMigrationState(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"id, version_id, is_applied", "pg_tables", "count(*)", "%I.%I", "ORDER BY schemaname, tablename", "\\gexec"} {
		if !strings.Contains(snapshotSQL, want) {
			t.Fatal(want)
		}
	}
}
func TestSQLScriptInput(t *testing.T) {
	t.Parallel()
	u, f, _ := fixture(t, true)
	f.Expect("docker", "exec", "-i", "old-pg").Return(counts, "", 0)
	if _, err := u.sql(context.Background(), "old-pg", snapshotSQL); err != nil {
		t.Fatal(err)
	}
	input, err := io.ReadAll(f.Calls()[0].Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if string(input) != snapshotSQL {
		t.Fatal(string(input))
	}
	f.AssertDone(t)
}

func TestBackendUsesReleaseCompose(t *testing.T) {
	t.Parallel()
	u, f, _ := fixture(t, false)
	want := u.base("exec", "-T", "postgres", "pgbackrest", "backup")
	f.Expect(want...)
	r := releaseRunner{Runner: f, u: u}
	if _, err := r.Run(context.Background(), remote.Cmd{Argv: []string{"docker", "compose", "-p", "demo", "exec", "-T", "postgres", "pgbackrest", "backup"}}); err != nil {
		t.Fatal(err)
	}
	f.AssertDone(t)
}

func TestNowOverrideAndProtectedCI(t *testing.T) {
	t.Parallel()
	u, f, _ := fixture(t, false)
	u.o.Dir = projectDir(t, "Sun 02:00-05:00", true)
	u.o.Now = true
	u.o.Confirm = project.Confirm{Yes: true, CI: true}
	f.Expect("mkdir", "/opt/demo/deploy.lock")
	f.ExpectUpload("/opt/demo/deploy.lock/owner.json")
	expectInspect(u, f, "17.11\n")
	f.Expect("sh", "-c")
	if err := Run(context.Background(), u.o, Deps{Remote: f, Now: func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) }}); err != nil {
		t.Fatal(err)
	}
	f.AssertDone(t)
}

func TestBackupFailureStopsMinor(t *testing.T) {
	t.Parallel()
	u, f, _ := fixture(t, false)
	f.Expect("sh", "-eu", "-c").Fail(errors.New("dump failed"))
	if err := u.minor(context.Background()); err == nil {
		t.Fatal("backup failure ignored")
	}
	if len(f.Uploads()) > 0 || len(f.Calls()) != 1 {
		t.Fatal("continued after failed backup")
	}
	f.AssertDone(t)
}

func TestSmokeRejectsWrongRelease(t *testing.T) {
	t.Parallel()
	u, f, _ := fixture(t, false)
	u.d.HTTPGet = func(_ context.Context, url string) (int, []byte, error) {
		return 200, []byte(`{"version":"other-release"}`), nil
	}
	u.d.Sleep = func(context.Context, time.Duration) error { return nil }
	if err := u.smoke(context.Background()); err == nil || !strings.Contains(err.Error(), "expected commit") {
		t.Fatalf("got %v", err)
	}
	f.AssertDone(t)
}

func TestMajorVolumeLayouts(t *testing.T) {
	t.Parallel()
	for _, to := range []int{16, 17, 18} {
		t.Run(strconv.Itoa(to), func(t *testing.T) {
			t.Parallel()
			u, f, _ := fixture(t, true)
			u.o.To = to
			image, err := pinnedImage(to)
			if err != nil {
				t.Fatal(err)
			}
			expectUpdate(u, f)
			if err = u.updateCompose(context.Background(), image, "demo_pgdata_"+strconv.Itoa(to)); err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			for _, upload := range f.Uploads() {
				if upload.Path == u.dir+"/compose.edge.yml" {
					if err = yaml.Unmarshal(upload.Data, &doc); err != nil {
						t.Fatal(err)
					}
				}
			}
			service := doc["services"].(map[string]any)["postgres"].(map[string]any)
			want := "postgres_data:/var/lib/postgresql/data"
			if to == 18 {
				want = "postgres_data:/var/lib/postgresql"
			}
			if service["volumes"].([]any)[0] != want {
				t.Fatal(service)
			}
			f.AssertDone(t)
		})
	}
}

func TestSmokeNormalizesPublicOrigin(t *testing.T) {
	t.Parallel()
	u, f, _ := fixture(t, false)
	u.env.PublicURL = "https://example.com/ignored?query=true"
	u.d.HTTPGet = func(_ context.Context, url string) (int, []byte, error) {
		if strings.Contains(url, "ignored") {
			t.Errorf("unexpected URL: %s", url)
		}
		return 200, []byte(`{"version":"abcdef123456"}`), nil
	}
	if err := u.smoke(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.AssertDone(t)
}
