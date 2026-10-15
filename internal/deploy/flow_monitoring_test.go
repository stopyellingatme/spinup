package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func monitoringFixture(t *testing.T, x *fx, profile, locked string, compose bool) {
	t.Helper()
	p := filepath.Join(x.root, "spinup.toml")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	data = append([]byte("modules = ['monitoring']\n"), data...)
	data = append(data, []byte("\n[monitoring]\nprofile = '"+profile+"'\n")...)
	if err := os.WriteFile(p, data, 0644); err != nil {
		t.Fatal(err)
	}
	lock := manifest.New()
	lock.Answers["monitoring_profile"] = locked
	if err := lock.Save(filepath.Join(x.root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	if compose {
		for name, data := range map[string]string{
			"compose.monitoring.yml": "services: {}\n",
			"grafana/entrypoint.sh":  "#!/bin/sh\nexec grafana\n",
		} {
			p := filepath.Join(x.root, "monitoring", name)
			if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
				t.Fatal(err)
			}
			mode := os.FileMode(0644)
			if strings.HasSuffix(name, ".sh") {
				mode = 0755
			}
			if err := os.WriteFile(p, []byte(data), mode); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestMonitoringPreflight(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, profile, locked, want string }{
		{"mismatch", "full", "lite", "set [monitoring] profile back"},
		{"disabled mismatch", "none", "full", "re-render the monitoring pack"},
		{"missing secret", "lite", "lite", "spinup secrets set --env staging GRAFANA_ADMIN_PASSWORD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			monitoringFixture(t, x, tc.profile, tc.locked, true)
			x.expectLocalGit()
			err := x.run()
			var ue *ui.Error
			if !errors.As(err, &ue) || !strings.Contains(ue.Try, tc.want) {
				t.Fatalf("error = %v", err)
			}
			if x.dialed != 0 || len(x.built) != 0 {
				t.Fatal("preflight changed the VM or built images")
			}
		})
	}
}

func TestMonitoringEnv(t *testing.T) {
	t.Parallel()
	cfg, err := project.Parse([]byte(`[monitoring]
heartbeat_url = 'https://check.example/ping?key=$literal'
alert_email = 'ops@example.com'
smtp_host = 'smtp.example.com:587'
smtp_user = 'the user'
smtp_from = 'Team "Ops" <ops@example.com>'
ntfy_url = 'https://ntfy.example/alerts'
prometheus_retention = '14d'
prometheus_retention_size = '2GB'
tempo_retention = '72h'
`))
	if err != nil {
		t.Fatal(err)
	}
	m := monitoringSettings(cfg)
	data, err := m.env("1000", "1001", "https://todo.example.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`DEPLOY_UID="1000"`, `DEPLOY_GID="1001"`, `PUBLIC_URL="https://todo.example.com"`,
		`HEARTBEAT_URL="https://check.example/ping?key=$$literal"`, `ALERT_EMAIL_TO="ops@example.com"`,
		`SMTP_HOST="smtp.example.com:587"`, `SMTP_USER="the user"`, `SMTP_FROM="Team \"Ops\" <ops@example.com>"`,
		`ALERT_NTFY_URL="https://ntfy.example/alerts"`, `PROMETHEUS_RETENTION="14d"`,
		`PROMETHEUS_RETENTION_SIZE="2GB"`, `TEMPO_RETENTION="72h"`,
	} {
		if !strings.Contains(string(data), want+"\n") {
			t.Errorf(".env misses %s:\n%s", want, data)
		}
	}
	cfg.Monitoring.SMTPFrom = "O'Brien \\ ops # team"
	m = monitoringSettings(cfg)
	data, err = m.env("1", "2", "")
	if err != nil || !strings.Contains(string(data), `SMTP_FROM="O'Brien \\ ops # team"`) {
		t.Fatalf("quoted .env = %s, %v", data, err)
	}
	cfg, _ = project.Parse(nil)
	m = monitoringSettings(cfg)
	data, err = m.env("1", "2", "")
	if err != nil || strings.Contains(string(data), "RETENTION") || strings.Contains(string(data), "SMTP_") || strings.Contains(string(data), "PUBLIC_URL") {
		t.Fatalf("defaults: %s, %v", data, err)
	}
}

func TestMonitoringResume(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, profile, skip   string
		module, compose, fail bool
	}{
		{name: "start", module: true, profile: "full", compose: true},
		{name: "failure", module: true, profile: "lite", compose: true, fail: true},
		{name: "no module", skip: "monitoring module is not selected"},
		{name: "none", module: true, profile: "none", skip: "profile is none"},
		{name: "no file", module: true, profile: "lite", skip: "monitoring/compose.monitoring.yml is missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			x.o.Resume = true
			if tc.module {
				monitoringFixture(t, x, tc.profile, tc.profile, tc.compose)
			}
			if tc.skip == "" {
				x.decrypted = map[string]string{"DATABASE_URL": "db", "POSTGRES_PASSWORD": "pg", "GRAFANA_ADMIN_PASSWORD": "grafana-secret"}
			}
			x.expectLocalGit()
			x.expectConnect()
			x.expectLock()
			journal := journalUpTo(t, "record")
			failed, _ := json.Marshal(Entry{Release: testRelease, Step: "monitoring", Status: "failed", At: testNow})
			journal += string(failed) + "\n"
			x.vm.Expect("sh", "-c").Return(journal, "", 0)
			rel, _ := json.Marshal(Release{Release: testRelease, Color: Green, Images: []Image{{Service: "api", Ref: "todo-api:" + testRelease}}})
			x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRelDir+"/release.json").Return(string(rel), "", 0)
			x.journal(1)
			if tc.skip == "" {
				x.ok("rm", "-rf", "--", testRoot+"/monitoring.new")
				x.ok("mkdir", "-p", "--", testRoot+"/monitoring.new", testRoot+"/monitoring.new/grafana")
				for _, path := range []string{"compose.monitoring.yml", "grafana/entrypoint.sh", ".env"} {
					x.vm.ExpectUpload(testRoot+"/monitoring.new/"+path).Return("", "", 0)
				}
				x.ok("sh", "-c", monitoringSwapScript, "spinup-monitoring", testRoot+"/monitoring")
				up := x.vm.Expect("docker", "compose", "-p", "todo-monitoring", "--project-directory", testRoot+"/monitoring", "-f", testRoot+"/monitoring/compose.monitoring.yml", "up", "-d", "--wait", "--remove-orphans")
				if tc.fail {
					up.Return("", "unhealthy", 1)
				} else {
					up.Return("", "", 0)
				}
			}
			x.journal(1)
			if !tc.fail {
				x.journal(2)
			}
			x.expectUnlockAndClose()
			err := x.run()
			if tc.fail {
				var ue *ui.Error
				if !errors.As(err, &ue) || !strings.Contains(err.Error(), "release is already recorded") || !strings.Contains(err.Error(), "not rolled back") || !strings.Contains(ue.Try, "deploy --resume --env staging") {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			x.vm.AssertDone(t)
			x.exec.AssertDone(t)
			entries := x.journalEntries()
			if entries[0].Step != "monitoring" || entries[0].Status != "started" {
				t.Fatalf("journal = %+v", entries)
			}
			if tc.skip != "" && entries[1].Detail != "skipped" {
				t.Fatalf("skip = %+v", entries[1])
			}
			if tc.fail && entries[1].Status != "failed" {
				t.Fatalf("failure = %+v", entries[1])
			}
			if tc.skip == "" {
				for _, u := range x.vm.Uploads() {
					if strings.HasSuffix(u.Path, "entrypoint.sh") && u.Mode != 0755 {
						t.Errorf("entrypoint mode = %v", u.Mode)
					}
					if strings.HasSuffix(u.Path, "/.env") && (u.Mode != 0600 || strings.Contains(string(u.Data), "grafana-secret")) {
						t.Errorf(".env mode/content = %v %s", u.Mode, u.Data)
					}
				}
				found := false
				for _, c := range x.vm.Calls() {
					if reflect.DeepEqual(c.Argv[:min(3, len(c.Argv))], []string{"docker", "compose", "-p"}) {
						found = true
						if c.Timeout != 5*time.Minute {
							t.Errorf("timeout = %v", c.Timeout)
						}
					}
				}
				if !found {
					t.Error("monitoring did not start")
				}
			}
			if len(x.built) != 0 || len(x.hookOpts) != 0 {
				t.Fatal("resume repeated previous steps")
			}
		})
	}
}

func TestMonitoringStepOrder(t *testing.T) {
	t.Parallel()
	if !strings.Contains(strings.Join(Steps, ","), "record,monitoring,post") {
		t.Fatalf("steps = %v", Steps)
	}
}

func TestMonitoringPlan(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		profile, want string
		compose       bool
	}{
		{"", "skipped (monitoring module is not selected)", false},
		{"none", "skipped (profile is none)", false},
		{"lite", "skipped (monitoring/compose.monitoring.yml is missing)", false},
		{"full", "start Compose project todo-monitoring", true},
	} {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			x.o.Plan = true
			if tc.profile != "" {
				monitoringFixture(t, x, tc.profile, tc.profile, tc.compose)
			}
			if tc.compose {
				x.decrypted = map[string]string{"DATABASE_URL": "db", "POSTGRES_PASSWORD": "pg", "GRAFANA_ADMIN_PASSWORD": "grafana-secret"}
			}
			x.expectLocalGit()
			x.expectConnect()
			x.expectUpstream("")
			x.expectReadCurrent("")
			x.vm.Expect("sh", "-c", readCurrentScript).Return("", "", 0)
			x.vm.Expect("sh", "-c").Return("", "", 3)
			x.vm.ExpectClose().Return("", "", 0)
			if err := x.run(); err != nil {
				t.Fatal(err)
			}
			x.vm.AssertDone(t)
			if !strings.Contains(x.out.String(), "Step monitoring (after record, before post): ") || !strings.Contains(x.out.String(), tc.want) {
				t.Fatalf("plan = %s", x.out.String())
			}
			if len(x.vm.Uploads()) != 0 || len(x.journalEvents()) != 0 {
				t.Fatal("plan changed the VM")
			}
		})
	}
}

func TestMonitoringSwap(t *testing.T) {
	t.Parallel()
	for _, previous := range []bool{false, true} {
		t.Run(fmt.Sprint(previous), func(t *testing.T) {
			t.Parallel()
			vm := localVM{root: t.TempDir()}
			files := map[string]string{"monitoring.new/compose.monitoring.yml": "new", "monitoring.old/stale": "stale"}
			if previous {
				files["monitoring/compose.monitoring.yml"] = "old"
			}
			writeTree(t, vm.root, files)
			run(t, vm, "sh", "-c", monitoringSwapScript, "spinup-monitoring", "/opt/app/monitoring")
			data, err := os.ReadFile(filepath.Join(vm.root, "monitoring/compose.monitoring.yml"))
			if err != nil || string(data) != "new" {
				t.Fatalf("installed tree = %s, %v", data, err)
			}
			if previous {
				data, err = os.ReadFile(filepath.Join(vm.root, "monitoring.old/compose.monitoring.yml"))
				if err != nil || string(data) != "old" {
					t.Fatalf("old tree = %s, %v", data, err)
				}
			}
			// A repeat with no staged tree must fail and restore the current tree.
			if _, err := vm.Run(context.Background(), remote.Cmd{Argv: []string{"sh", "-c", monitoringSwapScript, "spinup-monitoring", "/opt/app/monitoring"}}); err == nil {
				t.Fatal("missing upload succeeded")
			}
			data, err = os.ReadFile(filepath.Join(vm.root, "monitoring/compose.monitoring.yml"))
			if err != nil || string(data) != "new" {
				t.Fatalf("restored tree = %s, %v", data, err)
			}
		})
	}
}

func TestMonitoringEnvCompose(t *testing.T) {
	t.Parallel()
	if _, err := runexec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	cfg, _ := project.Parse(nil)
	cfg.Monitoring.SMTPFrom = "Team \"O'Brien\" \\ ops # <ops@example.com> $literal ${UNSET} $$ \t"
	m := monitoringSettings(cfg)
	data, err := m.env("1000", "1001", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		".env":        string(data),
		"compose.yml": "services:\n  test:\n    image: busybox:1\n    environment:\n      ROUNDTRIP: ${SMTP_FROM}\n",
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result, err := (runexec.OSRunner{}).Run(ctx, runexec.Cmd{Name: "docker", Args: []string{"compose", "-f", "compose.yml", "config", "--environment"}, Dir: dir})
	if err != nil {
		t.Fatalf("compose config: %v\n%s", err, result.Stderr)
	}
	var got string
	for _, line := range strings.Split(string(result.Stdout), "\n") {
		if strings.HasPrefix(line, "SMTP_FROM=") {
			got = strings.TrimPrefix(line, "SMTP_FROM=")
		}
	}
	if got != cfg.Monitoring.SMTPFrom {
		t.Errorf("parsed value = %q, want %q", got, cfg.Monitoring.SMTPFrom)
	}
}

func TestMonitoringInvalidFilesAndValues(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"symlink", "directory", "newline", "missing lock"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			monitoringFixture(t, x, "lite", "lite", true)
			switch name {
			case "symlink":
				if err := os.Symlink(filepath.Join(x.root, "spinup.toml"), filepath.Join(x.root, "monitoring", "outside")); err != nil {
					t.Fatal(err)
				}
			case "directory":
				p := filepath.Join(x.root, "monitoring", "compose.monitoring.yml")
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(p, 0755); err != nil {
					t.Fatal(err)
				}
			case "newline":
				p := filepath.Join(x.root, "spinup.toml")
				data, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, append(data, []byte("smtp_from = \"bad\\nINJECTED=value\"\n")...), 0644); err != nil {
					t.Fatal(err)
				}
			case "missing lock":
				if err := os.Remove(filepath.Join(x.root, "spinup.lock")); err != nil {
					t.Fatal(err)
				}
			}
			err := x.run()
			var ue *ui.Error
			if !errors.As(err, &ue) || ue.Try == "" || !strings.Contains(ue.What, "preflight") {
				t.Fatalf("error = %v", err)
			}
			if x.dialed != 0 || len(x.built) != 0 {
				t.Fatal("invalid monitoring input changed the VM")
			}
		})
	}
}
