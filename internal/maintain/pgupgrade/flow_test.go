package pgupgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/maintain/pg"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/remote"
	"go.yaml.in/yaml/v3"
)

const release = "/opt/demo/releases/20261015T062420Z-abcdef123456"
const op = "/opt/demo/pg-upgrade"
const edge = `services:
  postgres:
    image: demo-postgres:17.11
    environment:
      POSTGRES_USER: app
      POSTGRES_DB: app
      PGDATA: /var/lib/postgresql/data/pgdata
    volumes:
      - postgres_data:/var/lib/postgresql/data
volumes:
  postgres_data: {}
`

func write(t *testing.T, root, name, data string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (pg.Options, *remote.Fake, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	write(t, root, "spinup.toml", `modules = ["api", "deploy", "backups"]
[app]
name = "demo"
module = "example.com/demo"
[backups]
backend = "pgdump"
[env.production]
host = "vm.example.com"
user = "deploy"
public_url = "https://example.com"
protected = false
`)
	write(t, root, "deploy/environments/production.toml", "smoke_urls = [\"/ready\"]\n")
	out := &bytes.Buffer{}
	return pg.Options{Dir: root, Env: "production", To: 18, IKnow: true, Out: out, ErrOut: out}, &remote.Fake{}, out
}
func inspect(f *remote.Fake, major, data string) {
	f.Expect("cat", "/opt/demo/current/release.json").Return(`{"release":"20261015T062420Z-abcdef123456","color":"blue","git_sha":"abcdef123456abcdef"}`, "", 0)
	f.Expect("docker", "ps", "-q").Return("old-pg\n", "", 0)
	doc := map[string]any{"Image": "sha256:old", "Config": map[string]any{"Image": "demo-postgres:17.11", "User": "1000:1000", "Env": []string{"POSTGRES_USER=app", "POSTGRES_DB=app", "PGDATA=" + data}, "Labels": map[string]string{"com.docker.compose.project.config_files": release + "/compose.edge.yml"}}, "Mounts": []any{map[string]any{"Type": "volume", "Name": "demo_postgres_data", "Destination": "/var/lib/postgresql/data"}}}
	if major == "18.6" {
		doc["Mounts"] = []any{map[string]any{"Type": "volume", "Name": "demo_postgres_data", "Destination": "/var/lib/postgresql"}}
	}
	raw, _ := json.Marshal(doc)
	f.Expect("docker", "inspect").Return(string(raw), "", 0)
	f.Expect("docker", "exec", "-i", "old-pg").Return(major+"\n", "", 0)
}
func lock(f *remote.Fake) {
	f.Expect("mkdir", "/opt/demo/deploy.lock")
	f.ExpectUpload("/opt/demo/deploy.lock/owner.json")
}
func backupMetrics(f *remote.Fake) {
	f.Expect("mkdir", "-p", "/opt/demo/backups/metrics")
	f.Expect("chmod", "0755")
	f.Expect("flock")
}
func beforeCheck(f *remote.Fake, helper string) {
	f.ExpectUpload(release + "/Dockerfile.pg-upgrade")
	f.Expect("docker", "build", "--tag", helper)
	f.Expect("sh", "-eu", "-c").Return("512\n", "", 0)
	backupMetrics(f)
	f.Expect("mkdir", "-m", "0700", op)
	for _, file := range []string{"compose.edge.yml", "Dockerfile.postgres", ".env"} {
		f.Expect("cp", "--", release+"/"+file, op+"/"+file)
	}
	f.ExpectUpload(op + "/rollback.sh")
	f.Expect("docker", "create", "--name")
	f.ExpectUpload(op + "/state.json")
	f.ExpectUpload(op + "/link-state.json")
	f.Expect("docker", "exec", "-i", "old-pg").Return("UTF8\nC.UTF-8\nC.UTF-8\noff\n", "", 0)
	f.Expect("sh", "-ec")                                         // stop both colors
	f.Expect("docker", "compose", "-p", "demo").Return("", "", 0) // stop postgres
	f.Expect("docker", "run", "--rm")                             // initdb
}
func afterLink(f *remote.Fake, failStart bool) {
	f.Expect("docker", "run", "--rm") // disable the original cluster
	f.Expect("docker", "rm", "old-pg")
	f.Expect("cat", release+"/compose.edge.yml").Return(edge, "", 0)
	f.Expect("cat", release+"/Dockerfile.postgres").Return("FROM postgres:17.11\nRUN echo backups\n", "", 0)
	f.ExpectUpload(release + "/Dockerfile.postgres")
	f.ExpectUpload(release + "/compose.edge.yml")
	f.Expect("docker", "compose", "-p", "demo")      // build
	e := f.Expect("docker", "compose", "-p", "demo") // start postgres
	if failStart {
		e.Fail(errors.New("health failed"))
		return
	}
	f.Expect("docker", "compose", "-p", "demo")      // analyze
	f.Expect("docker", "compose", "-p", "demo-blue") // migrate
	f.Expect("docker", "compose", "-p", "demo-blue") // start live
	f.ExpectUpload(op + "/link-state.json")
}

func TestUpgradeOrderAndFailures(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"success", "check", "link", "start"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			o, f, out := fixture(t)
			lock(f)
			inspect(f, "17.11", "/var/lib/postgresql/data/pgdata")
			beforeCheck(f, "demo-pg-upgrade:17-18")
			check := f.Expect("docker", "run", "--rm")
			if stage == "check" {
				check.Fail(errors.New("extension missing"))
			} else {
				link := f.Expect("docker", "run", "--rm")
				if stage == "link" {
					link.Fail(errors.New("link failed"))
				} else {
					afterLink(f, stage == "start")
				}
			}
			f.Expect("sh", "-c") // release lock on every path
			var urls []string
			err := Run(context.Background(), o, pg.Deps{Remote: f, HTTPGet: func(_ context.Context, url string) (int, []byte, error) {
				urls = append(urls, url)
				return 200, []byte(`{"version":"abcdef123456"}`), nil
			}})
			if stage == "success" {
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(urls, []string{"https://example.com/healthz", "https://example.com/ready", "https://example.com/version"}) {
					t.Fatal(urls)
				}
			} else if err == nil || !strings.Contains(err.Error(), "spinup backup restore") || !strings.Contains(err.Error(), "--into new") {
				t.Fatalf("got %v", err)
			}
			f.AssertDone(t)
			var checks, links int
			for _, c := range f.Calls() {
				if slices.Contains(c.Argv, "pg_upgrade") {
					if slices.Contains(c.Argv, "--check") {
						checks++
					} else {
						links++
					}
					if !slices.Contains(c.Argv, "--link") || !slices.Contains(c.Argv, "type=volume,src=demo_postgres_data,dst=/pg") {
						t.Fatal(c.Argv)
					}
				}
				if stage == "check" && (slices.Contains(c.Argv, "build") && slices.Contains(c.Argv, "postgres") || slices.Contains(c.Argv, "rm")) {
					t.Fatal("check failure changed the service", c.Argv)
				}
			}
			if checks != 1 || (stage == "check" && links != 0) || (stage != "check" && links != 1) {
				t.Fatalf("checks %d links %d", checks, links)
			}
			for _, u := range f.Uploads() {
				switch u.Path {
				case op + "/rollback.sh":
					script := filepath.Join(t.TempDir(), "rollback.sh")
					if err = os.WriteFile(script, u.Data, 0700); err != nil {
						t.Fatal(err)
					}
					// The guard executes before any VM command, even if a new server has started.
					r, e := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "/bin/sh", Args: []string{script}, Env: []string{"PATH=/nonexistent"}})
					if e == nil || !bytes.Contains(r.Stderr, []byte("Never start the old cluster")) || !bytes.Contains(r.Stderr, []byte("--label")) {
						t.Fatalf("recovery ran: %s, %v", r.Stderr, e)
					}
				case release + "/compose.edge.yml":
					var doc map[string]any
					if err = yaml.Unmarshal(u.Data, &doc); err != nil {
						t.Fatal(err)
					}
					p := doc["services"].(map[string]any)["postgres"].(map[string]any)
					if p["environment"].(map[string]any)["PGDATA"] != "/var/lib/postgresql/18/docker" {
						t.Fatal(p)
					}
					v := doc["volumes"].(map[string]any)["postgres_data"].(map[string]any)
					if v["name"] != "demo_postgres_data" || v["external"] != true {
						t.Fatal(v)
					}
				}
			}
			if stage != "success" && !strings.Contains(out.String(), "Never start the old cluster") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestRefusalsAndPlan(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"plan", "cleanup plan", "missing confirmation", "window", "protected", "unsupported", "downgrade", "layout"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			o, f, out := fixture(t)
			switch name {
			case "plan":
				o.Plan = true
			case "cleanup plan":
				o.Plan = true
				o.Cleanup = true
				o.To = 0
			case "missing confirmation":
				o.IKnow = false
			case "window":
				p := filepath.Join(o.Dir, "spinup.toml")
				b, _ := os.ReadFile(p)
				write(t, o.Dir, "spinup.toml", string(b)+"\n[maintenance]\nwindow = \"Sun 02:00-05:00\"\n")
			case "protected":
				p := filepath.Join(o.Dir, "spinup.toml")
				b, _ := os.ReadFile(p)
				write(t, o.Dir, "spinup.toml", strings.Replace(string(b), "protected = false", "protected = true", 1))
			case "unsupported":
				o.To = 99
			case "downgrade":
				o.To = 17
				lock(f)
				inspect(f, "17.11", "/var/lib/postgresql/data/pgdata")
				f.Expect("sh", "-c")
			case "layout":
				lock(f)
				inspect(f, "17.11", "/outside/data")
				f.Expect("sh", "-c")
			}
			err := Run(context.Background(), o, pg.Deps{Remote: f, Now: func() time.Time { return time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC) }})
			if o.Plan {
				if err != nil || len(f.Calls()) != 0 || !strings.Contains(out.String(), "Plan:") {
					t.Fatalf("%v %s", err, out)
				}
			} else if err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("got %v", err)
			}
			f.AssertDone(t)
		})
	}
}

func TestCleanup(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"success", "incomplete", "overlap", "wrong active", "missing repository"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			o, f, _ := fixture(t)
			o.Cleanup = true
			o.To = 0
			lock(f)
			st := state{Volume: "demo_postgres_data", Old: "pgdata", New: "18/docker", From: 17, To: 18, Restore: "spinup backup restore --label safety --into new", Complete: true}
			switch name {
			case "incomplete":
				st.Complete = false
			case "overlap":
				st.Old = "18"
			}
			raw, _ := json.Marshal(st)
			f.Expect("cat", op+"/link-state.json").Return(string(raw), "", 0)
			if name != "incomplete" && name != "overlap" {
				data := "/var/lib/postgresql/18/docker"
				if name == "wrong active" {
					data = "/var/lib/postgresql/pgdata"
				}
				inspect(f, "18.6", data)
				if name != "missing repository" {
					l := manifest.New()
					l.Answers["db_version"] = "18"
					if err := l.Save(filepath.Join(o.Dir, "spinup.lock")); err != nil {
						t.Fatal(err)
					}
					write(t, o.Dir, "deploy/compose.edge.yml", "services:\n  postgres:\n    environment:\n      PGDATA: /var/lib/postgresql/18/docker\nvolumes:\n  postgres_data:\n    external: true\n    name: demo_postgres_data\n")
				}
				if name == "success" {
					f.Expect("docker", "run", "--rm")
				}
			}
			f.Expect("sh", "-c")
			err := Run(context.Background(), o, pg.Deps{Remote: f})
			if name == "success" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("unsafe cleanup succeeded")
			}
			for _, c := range f.Calls() {
				if slices.Contains(c.Argv, "volume") || slices.Contains(c.Argv, "start") {
					t.Fatal("cleanup touched the volume or old server", c.Argv)
				}
			}
			f.AssertDone(t)
		})
	}
}

func TestBackupFailureKeepsAppRunning(t *testing.T) {
	t.Parallel()
	o, f, _ := fixture(t)
	lock(f)
	inspect(f, "17.11", "/var/lib/postgresql/data/pgdata")
	f.ExpectUpload(release + "/Dockerfile.pg-upgrade")
	f.Expect("docker", "build", "--tag")
	f.Expect("sh", "-eu", "-c").Fail(errors.New("backup storage unavailable"))
	f.Expect("sh", "-c")
	err := Run(context.Background(), o, pg.Deps{Remote: f})
	if err == nil || !strings.Contains(err.Error(), "backup storage unavailable") {
		t.Fatalf("got %v", err)
	}
	for _, c := range f.Calls() {
		if slices.Contains(c.Argv, "stop") || slices.Contains(c.Argv, "pg_upgrade") {
			t.Fatal("backup failure started downtime", c.Argv)
		}
	}
	f.AssertDone(t)
}

func TestUpgradeTo17UsesSameVolume(t *testing.T) {
	t.Parallel()
	o, f, _ := fixture(t)
	o.To = 17
	lock(f)
	inspect(f, "16.15", "/var/lib/postgresql/data/pgdata")
	beforeCheck(f, "demo-pg-upgrade:16-17")
	f.Expect("docker", "run", "--rm") // check
	f.Expect("docker", "run", "--rm") // link
	afterLink(f, false)
	f.Expect("sh", "-c")
	err := Run(context.Background(), o, pg.Deps{Remote: f, HTTPGet: func(context.Context, string) (int, []byte, error) {
		return 200, []byte(`{"version":"abcdef123456"}`), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range f.Uploads() {
		if u.Path == release+"/compose.edge.yml" {
			var doc map[string]any
			if err = yaml.Unmarshal(u.Data, &doc); err != nil {
				t.Fatal(err)
			}
			p := doc["services"].(map[string]any)["postgres"].(map[string]any)
			if p["environment"].(map[string]any)["PGDATA"] != "/var/lib/postgresql/data/17/docker" {
				t.Fatal(p)
			}
			if p["volumes"].([]any)[0] != "postgres_data:/var/lib/postgresql/data" {
				t.Fatal(p)
			}
		}
	}
	for _, c := range f.Calls() {
		if slices.Contains(c.Argv, "initdb") && (slices.Contains(c.Argv, "--no-data-checksums") || slices.Contains(c.Argv, "--data-checksums")) {
			t.Fatal("changed source checksums", c.Argv)
		}
		if slices.Contains(c.Argv, "pg_upgrade") && !slices.Contains(c.Argv, "--new-datadir=/pg/17/docker") {
			t.Fatal(c.Argv)
		}
	}
	f.AssertDone(t)
}
