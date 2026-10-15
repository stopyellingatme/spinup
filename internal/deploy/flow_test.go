package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/db/lint"
	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/ui"
)

var allSteps = []string{
	"preflight:done", "build:done",
	"hooks:started", "hooks:done", "transport:started", "transport:done", "upload:started", "upload:done",
	"edge:started", "edge:done", "migrate:started", "migrate:done", "switch:started", "switch:done",
	"smoke:started", "smoke:done", "record:started", "record:done", "post:started", "post:done",
}

// TestFlowSSHFirstDeploy runs a first deploy of an API-only app with the ssh transport.
func TestFlowSSHFirstDeploy(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	x.expectLocalGit()
	x.expectConnect()
	x.expectLock()
	x.expectBegin("")
	// hooks
	x.journal(1)
	x.expectReadCurrent("")
	x.journal(1)
	// transport
	x.journal(1)
	x.exec.Expect("docker", "image", "inspect", "--format", "{{.Id}} "+imageFingerprint, "todo-api:"+testRelease).Return("sha256:aaa linux/amd64 [\"sha256:l1\"]\n", "", 0)
	x.vm.Expect("docker", "image", "inspect", "--format", imageFingerprint, "todo-api:"+testRelease).Return("", "No such image", 1)
	x.exec.Expect("docker", "save", "-o").Return("", "", 0)
	x.ok("docker", "load")
	x.vm.Expect("docker", "image", "inspect", "--format", imageFingerprint, "todo-api:"+testRelease).Return("linux/amd64 [\"sha256:l1\"]\n", "", 0)
	x.journal(1)
	// upload
	x.journal(1)
	x.expectUploadRelease("")
	x.journal(1)
	// edge
	x.journal(1)
	x.expectEdge("", "")
	x.journal(1)
	// migrate
	x.journal(1)
	x.expectMigrate()
	x.journal(1)
	// switch
	x.journal(1)
	x.expectSwitch(string(PlaceholderUpstream), Blue, false)
	x.journal(1)
	// smoke
	x.journal(1)
	x.expectSmoke(Blue)
	x.journal(1)
	// record
	x.journal(1)
	x.expectRecord("", testRelease+"\n")
	x.journal(1)
	// post
	x.journal(2)
	x.expectUnlockAndClose()

	if err := x.run(); err != nil {
		t.Fatalf("deploy: %v\n%s", err, x.out.String())
	}
	x.vm.AssertDone(t)
	x.exec.AssertDone(t)
	if got := x.journalEvents(); !reflect.DeepEqual(got, allSteps) {
		t.Fatalf("journal = %v", got)
	}
	if len(x.built) != 1 || x.built[0].Tag != testRelease || x.built[0].Push || !reflect.DeepEqual(x.built[0].Targets, []string{"api"}) || x.built[0].PublicOrigin != "https://todo.example.com" || x.built[0].Platform != "linux/amd64" {
		t.Fatalf("build = %#v", x.built)
	}
	// The hook sees the deploy flags and all migrations as pending on the first deploy.
	if len(x.hookOpts) != 1 || x.hookOpts[0].SkipBackup || !reflect.DeepEqual(x.hookOpts[0].PendingMigrations, []string{"00001_init.sql", "00002_more.sql"}) || x.hookCtx[0].Release != testRelease || x.hookCtx[0].App != "todo" {
		t.Fatalf("hook = %#v %#v", x.hookOpts, x.hookCtx)
	}
	env := string(x.upload(testRelDir + "/.env"))
	for _, want := range []string{"SPINUP_COLOR=blue\n", "API_IMAGE=todo-api:" + testRelease + "\n", "PUBLIC_ORIGIN=https://todo.example.com\n", "PUBLIC_HOST=todo.example.com\n", "DEPLOY_UID=1000\n", "DEPLOY_GID=1001\n", "API_REPLICAS=2\n", "API_CPUS=0.5\n", "API_MEMORY=256m\n", "POSTGRES_MEMORY=1g\n"} {
		if !strings.Contains(env, want) {
			t.Errorf(".env misses %q:\n%s", want, env)
		}
	}
	if strings.Contains(env, "WEB_") {
		t.Errorf("API-only .env has web variables:\n%s", env)
	}
	var rel Release
	if err := json.Unmarshal(x.upload(testRelDir+"/release.json"), &rel); err != nil {
		t.Fatal(err)
	}
	if rel.Release != testRelease || rel.GitSHA != testSHA || rel.Color != Blue || rel.Transport != "ssh" || rel.MigrationVersion != "2" || rel.User != "alice" || !rel.CreatedAt.Equal(testNow) || len(rel.Images) != 1 || rel.Images[0].ID != "sha256:aaa" || rel.Images[0].Ref != "todo-api:"+testRelease {
		t.Fatalf("release.json = %#v", rel)
	}
	if !reflect.DeepEqual(rel.Secrets, SecretHashes(testSecrets)) || len(rel.Secrets["DATABASE_URL"]) != 64 {
		t.Fatalf("release secrets = %#v", rel.Secrets)
	}
	for _, u := range x.vm.Uploads() {
		if strings.HasPrefix(u.Path, testRoot+"/secrets/") {
			name := strings.TrimPrefix(u.Path, testRoot+"/secrets/")
			if u.Mode != 0600 || string(u.Data) != testSecrets[name] {
				t.Errorf("secret upload %s mode %v", u.Path, u.Mode)
			}
		}
	}
	if got := string(x.upload(testRoot + "/caddy/upstream.caddy")); got != string(PlaceholderUpstream) {
		t.Fatalf("first upstream = %q", got)
	}
	var history []ReleaseEntry
	if err := json.Unmarshal(x.upload(testRoot+"/releases.json"), &history); err != nil || len(history) != 1 || history[0].Release != testRelease {
		t.Fatalf("releases.json = %#v, %v", history, err)
	}
	// The release directory never holds local-only or environment files.
	for _, u := range x.vm.Uploads() {
		for _, bad := range []string{"compose.dev.yml", "Dockerfile.api", "environments", "cloud-init", ".gitkeep"} {
			if strings.Contains(u.Path, bad) {
				t.Errorf("uploaded %s", u.Path)
			}
		}
	}
	if !reflect.DeepEqual(x.sleeps, []time.Duration{smokeInterval}) {
		t.Fatalf("sleeps = %v", x.sleeps)
	}
	if !strings.Contains(x.out.String(), "Deployed release "+testRelease+" to staging (blue color)") {
		t.Fatalf("output = %s", x.out.String())
	}
	x.assertNoSecrets()
}

// TestFlowRegistrySecondDeploy runs a full app with the registry transport while blue is live.
func TestFlowRegistrySecondDeploy(t *testing.T) {
	t.Parallel()
	x := newFx(t, true, "registry")
	x.o.SkipBackup = true
	current, _ := json.Marshal(Release{Release: "20261014T080000Z-ffffffffffff", Color: Blue, MigrationVersion: "1", Secrets: map[string]string{"DATABASE_URL": "old"}})
	x.expectLocalGit()
	x.expectConnect()
	x.expectLock()
	x.expectBegin("")
	x.journal(1)
	x.expectReadCurrent(string(current))
	x.journal(1)
	x.journal(1)
	x.ok("docker", "pull", "--quiet", "ghcr.io/acme/todo-api@"+digestAPI)
	x.ok("docker", "pull", "--quiet", "ghcr.io/acme/todo-web@"+digestWeb)
	x.journal(1)
	x.journal(1)
	x.expectUploadRelease(string(UpstreamFile(Blue)))
	x.journal(1)
	x.journal(1)
	x.expectEdge(string(UpstreamFile(Blue)), "caddy1\n")
	x.journal(1)
	x.journal(1)
	x.expectMigrate()
	x.journal(1)
	x.journal(1)
	x.expectSwitch(string(UpstreamFile(Blue)), Green, true)
	x.journal(1)
	x.journal(1)
	x.expectSmoke(Green)
	x.journal(1)
	x.journal(1)
	var history []ReleaseEntry
	for i := range 5 {
		id := "2026100" + string(rune('9'-i)) + "T080000Z-eeeeeeeeeeee"
		history = append(history, ReleaseEntry{Release: id, Images: []Image{{Service: "api", Ref: "ghcr.io/acme/todo-api@sha256:old" + id}}})
	}
	data, _ := json.Marshal(history)
	pruned := history[4].Release
	x.expectRecord(string(data), strings.Join([]string{history[0].Release, pruned, testRelease, "lost+found"}, "\n"))
	x.ok("rm", "-rf", "--", testRoot+"/releases/"+pruned)
	x.ok("docker", "image", "rm", "ghcr.io/acme/todo-api@sha256:old"+pruned)
	x.journal(1)
	x.journal(2)
	x.expectUnlockAndClose()

	if err := x.run(); err != nil {
		t.Fatalf("deploy: %v\n%s", err, x.out.String())
	}
	x.vm.AssertDone(t)
	x.exec.AssertDone(t)
	if got := x.journalEvents(); !reflect.DeepEqual(got, allSteps) {
		t.Fatalf("journal = %v", got)
	}
	if !x.built[0].Push || !reflect.DeepEqual(x.built[0].Targets, []string{"api", "web"}) {
		t.Fatalf("build = %#v", x.built)
	}
	if !x.hookOpts[0].SkipBackup || !reflect.DeepEqual(x.hookOpts[0].PendingMigrations, []string{"00002_more.sql"}) {
		t.Fatalf("hook options = %#v", x.hookOpts)
	}
	env := string(x.upload(testRelDir + "/.env"))
	for _, want := range []string{"SPINUP_COLOR=green\n", "API_IMAGE=ghcr.io/acme/todo-api@" + digestAPI + "\n", "WEB_IMAGE=ghcr.io/acme/todo-web@" + digestWeb + "\n", "WEB_REPLICAS=1\n"} {
		if !strings.Contains(env, want) {
			t.Errorf(".env misses %q:\n%s", want, env)
		}
	}
	if got := string(x.upload(testRoot + "/caddy/upstream.caddy")); LiveColor([]byte(got)) != Green {
		t.Fatalf("upstream = %q", got)
	}
	var kept []ReleaseEntry
	if err := json.Unmarshal(x.upload(testRoot+"/releases.json"), &kept); err != nil || len(kept) != KeepReleases || kept[0].Release != testRelease || kept[0].Color != Green || kept[4].Release != history[3].Release {
		t.Fatalf("releases.json = %#v, %v", kept, err)
	}
	// Drain: the minimum wait, one poll interval, then the old address was idle. Smoke: one retry.
	if !reflect.DeepEqual(x.sleeps, []time.Duration{drainMin, drainInterval, smokeInterval}) {
		t.Fatalf("sleeps = %v", x.sleeps)
	}
	if strings.Contains(strings.Join(x.httpLog, " "), "/version") {
		t.Fatalf("a full app read /version on the public URL: %v", x.httpLog)
	}
	x.assertNoSecrets()
}

func TestFlowHealthFailureRollsBack(t *testing.T) {
	t.Parallel()
	for _, noRollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "auto rollback", true: "no auto rollback"}[noRollback], func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			x.o.NoAutoRollback = noRollback
			x.o.Resume = true
			// Resume a release whose journal ends after migrate.
			x.expectLocalGit()
			x.expectConnect()
			x.expectLock()
			x.vm.Expect("sh", "-c").Return(journalUpTo(t, "migrate"), "", 0)
			rel, _ := json.Marshal(Release{Release: testRelease, Color: Green, Images: []Image{{Service: "api", Tag: "todo-api:" + testRelease, Ref: "todo-api:" + testRelease}}})
			x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRelDir+"/release.json").Return(string(rel), "", 0)
			x.journal(1)
			x.expectUpstream(string(UpstreamFile(Blue)))
			x.vm.Expect(psArgv("todo", "caddy")...).Return("caddy1\n", "", 0)
			x.ok(x.colorArgv(Green, "up", "-d", "--wait", "--wait-timeout", "4", "--remove-orphans")...)
			// Two readiness attempts (4 s timeout, 2 s interval). Both fail.
			for range 2 {
				x.vm.Expect(psArgv("todo-green", "api")...).Return("napi1\n", "", 0)
				x.vm.Expect(inspectArgv("napi1")...).Return("10.0.1.1\n", "", 0)
				x.vm.Expect(wgetReady("10.0.1.1", 8080, "/readyz")...).Return("", "server returned error: HTTP/1.1 503", 1)
			}
			if !noRollback {
				x.ok(x.colorArgv(Green, "down", "-t", "15")...)
			}
			x.journal(1)
			x.expectUnlockAndClose()

			err := x.run()
			var health *HealthError
			if !errors.As(err, &health) || health.Stopped == noRollback || ui.ExitCode(err) != ui.ExitError {
				t.Fatalf("error = %v", err)
			}
			var ue *ui.Error
			if !errors.As(err, &ue) || ue.What != "Deploy failed at step switch" || !strings.Contains(ue.Try, "server logs") {
				t.Fatalf("user error = %#v", ue)
			}
			x.vm.AssertDone(t)
			x.exec.AssertDone(t)
			events := x.journalEvents()
			if !reflect.DeepEqual(events, []string{"switch:started", "switch:failed"}) {
				t.Fatalf("journal = %v", events)
			}
			entries := x.journalEntries()
			if !strings.Contains(entries[1].Detail, "did not become healthy") {
				t.Fatalf("failure detail = %q", entries[1].Detail)
			}
			// Caddy was never switched.
			for _, u := range x.vm.Uploads() {
				if strings.HasSuffix(u.Path, "upstream.caddy") {
					t.Fatalf("upstream changed: %s", u.Path)
				}
			}
			x.assertNoSecrets()
		})
	}
}

// journalUpTo returns a journal of testRelease with every step up to and including last done.
func journalUpTo(t *testing.T, last string) string {
	t.Helper()
	var b strings.Builder
	images, _ := json.Marshal([]Image{{Service: "api", Tag: "todo-api:" + testRelease, Ref: "todo-api:" + testRelease, ID: "sha256:aaa"}})
	for _, step := range Steps {
		detail := ""
		if step == "build" || step == "transport" {
			detail = string(images)
		}
		for _, status := range []string{"started", "done"} {
			data, err := json.Marshal(Entry{Release: testRelease, Step: step, Status: status, At: testNow, Detail: detail})
			if err != nil {
				t.Fatal(err)
			}
			b.Write(append(data, '\n'))
		}
		if step == last {
			return b.String()
		}
	}
	return b.String()
}

func TestFlowResumeFromFailedStep(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	x.o.Resume = true
	journal := journalUpTo(t, "switch")
	failed, _ := json.Marshal(Entry{Release: testRelease, Step: "smoke", Status: "failed", At: testNow, Detail: "GET answered 502"})
	journal += string(failed) + "\n"
	rel, _ := json.Marshal(Release{Release: testRelease, Color: Green, Images: []Image{{Service: "api", Tag: "todo-api:" + testRelease, Ref: "todo-api:" + testRelease}}})
	x.expectLocalGit()
	x.expectConnect()
	x.expectLock()
	x.vm.Expect("sh", "-c").Return(journal, "", 0)
	x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRelDir+"/release.json").Return(string(rel), "", 0)
	x.journal(1)
	x.expectSmoke(Green)
	x.journal(1)
	x.journal(1)
	x.expectRecord("", testRelease+"\n")
	x.journal(1)
	x.journal(2)
	x.expectUnlockAndClose()

	if err := x.run(); err != nil {
		t.Fatalf("resume: %v\n%s", err, x.out.String())
	}
	x.vm.AssertDone(t)
	x.exec.AssertDone(t)
	if len(x.built) != 0 || len(x.hookOpts) != 0 {
		t.Fatalf("resume repeated done steps: build %v, hooks %v", x.built, x.hookOpts)
	}
	want := []string{"smoke:started", "smoke:done", "record:started", "record:done", "post:started", "post:done"}
	if got := x.journalEvents(); !reflect.DeepEqual(got, want) {
		t.Fatalf("journal = %v", got)
	}
	if !strings.Contains(x.out.String(), "Resuming release "+testRelease+" at step smoke") || !strings.Contains(x.out.String(), "(green color)") {
		t.Fatalf("output = %s", x.out.String())
	}
}

func TestFlowResumeRefusals(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, journal, want string
		code                int
	}{
		{"empty journal", "", "Nothing to resume", ui.ExitError},
		{"other commit", strings.ReplaceAll(journalUpTo(t, "hooks"), testRelease, "20261015T080000Z-aaaaaaaaaaaa"), "Cannot resume", ui.ExitError},
		{"complete", journalUpTo(t, "post"), "", ui.ExitOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			x.o.Resume = true
			x.expectLocalGit()
			x.expectConnect()
			x.expectLock()
			x.vm.Expect("sh", "-c").Return(tt.journal, "", 0)
			x.expectUnlockAndClose()
			err := x.run()
			if ui.ExitCode(err) != tt.code || (tt.want != "" && !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("error = %v", err)
			}
			x.vm.AssertDone(t)
			if len(x.journalEvents()) != 0 {
				t.Fatal("a refused resume wrote the journal")
			}
		})
	}
}

func TestFlowLockHeld(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	held, _ := json.Marshal(LockInfo{Owner: "bob", PID: 7, Host: "ci", Acquired: testNow.Add(-time.Minute), TTLSeconds: 1800, Release: "other"})
	x.expectLocalGit()
	x.expectConnect()
	x.vm.Expect("mkdir", testRoot+"/deploy.lock").Return("", "File exists", 1)
	x.vm.Expect("sh", "-c").Return(string(held), "", 0)
	x.vm.ExpectClose().Return("", "", 0)
	err := x.run()
	var locked *LockedError
	if !errors.As(err, &locked) || ui.ExitCode(err) != ui.ExitLocked || !strings.Contains(err.Error(), "bob") {
		t.Fatalf("error = %v (exit %d)", err, ui.ExitCode(err))
	}
	x.vm.AssertDone(t)
	if len(x.journalEvents()) != 0 || len(x.vm.Uploads()) != 0 {
		t.Fatal("a held lock still changed the VM")
	}
}

// TestFlowStepFailureJournaledAndUnlocked fails the migrations and checks the journal and the lock.
func TestFlowStepFailureJournaledAndUnlocked(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	x.o.Resume = true
	rel, _ := json.Marshal(Release{Release: testRelease, Color: Blue, Images: []Image{{Service: "api", Ref: "todo-api:" + testRelease}}})
	x.expectLocalGit()
	x.expectConnect()
	x.expectLock()
	x.vm.Expect("sh", "-c").Return(journalUpTo(t, "edge"), "", 0)
	x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRelDir+"/release.json").Return(string(rel), "", 0)
	x.journal(1)
	x.vm.Expect(ComposeArgv("todo-migrate", testRelDir, []string{testRelDir + "/compose.yml"}, "--profile", "migrate", "run")...).Return("", "connect to postgres://app:hunter2-db-pass@postgres failed", 1)
	x.journal(1)
	x.expectUnlockAndClose()
	err := x.run()
	var ue *ui.Error
	if !errors.As(err, &ue) || ue.What != "Deploy failed at step migrate" || !strings.Contains(ue.Try, "deploy --resume --env staging") {
		t.Fatalf("error = %v", err)
	}
	x.vm.AssertDone(t)
	entries := x.journalEntries()
	if len(entries) != 2 || entries[1].Status != "failed" || entries[1].Step != "migrate" || !strings.Contains(entries[1].Detail, "old release is still live") {
		t.Fatalf("journal = %#v", entries)
	}
	if strings.Contains(err.Error(), "hunter2") || strings.Contains(entries[1].Detail, "hunter2") {
		t.Fatalf("secret in error: %v / %s", err, entries[1].Detail)
	}
	x.assertNoSecrets()
}

func TestFlowPlanMakesNoWrite(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	x.o.Plan = true
	x.o.Confirm = project.Confirm{} // --plan never asks
	current := Release{Release: "20261014T080000Z-ffffffffffff", Color: Blue, MigrationVersion: "1", Images: []Image{{Service: "api", Ref: "todo-api:20261014T080000Z-ffffffffffff"}}, Secrets: SecretHashes(map[string]string{"DATABASE_URL": "old-value", "POSTGRES_PASSWORD": testSecrets["POSTGRES_PASSWORD"], "REMOVED": "x"})}
	currentJSON, _ := json.Marshal(current)
	tarball := tarOf(t, map[string]string{
		"compose.yml":      "name: todo-${SPINUP_COLOR}\nold: true\n",
		"compose.edge.yml": "name: todo\n",
		"release.json":     string(currentJSON),
		"old.caddy":        "x",
	})
	x.expectLocalGit()
	x.expectConnect()
	x.expectUpstream(string(UpstreamFile(Blue)))
	x.expectReadCurrent(string(currentJSON))
	x.vm.Expect("sh", "-c", readCurrentScript, "spinup-current", testRoot+"/current").Return(tarball, "", 0)
	x.vm.Expect("sh", "-c").Return("", "", 3) // no lock
	x.vm.ExpectClose().Return("", "", 0)
	if err := x.run(); err != nil {
		t.Fatalf("plan: %v", err)
	}
	x.vm.AssertDone(t)
	x.exec.AssertDone(t)
	if len(x.vm.Uploads()) != 0 || len(x.built) != 0 || len(x.journalEvents()) != 0 || len(x.hookOpts) != 0 {
		t.Fatal("--plan changed something")
	}
	for _, c := range x.vm.Calls() {
		if c.Argv[0] != "sh" {
			t.Fatalf("--plan ran %v", c.Argv)
		}
	}
	out := x.out.String()
	for _, want := range []string{
		"Plan: deploy release " + testRelease + " to staging (vm.example.com)",
		"Color: blue -> green",
		"api: todo-api:20261014T080000Z-ffffffffffff -> todo-api:" + testRelease,
		"Migrations: 1 pending\n  00002_more.sql",
		"  added API_TOKEN", "  changed DATABASE_URL", "  removed REMOVED",
		"  changed compose.yml", "-old: true", "  added Caddyfile", "  removed old.caddy", "  added upstream.caddy",
		"Nothing changed on the VM (--plan).",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "compose.edge.yml") || strings.Contains(out, "release.json") {
		t.Errorf("plan lists unchanged files:\n%s", out)
	}
	x.assertNoSecrets()
}

func TestFlowProtectedConfirmation(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		confirm project.Confirm
		dialed  bool
		want    string
	}{
		{"no terminal", project.Confirm{}, false, "Deploy not confirmed"},
		{"wrong name", project.Confirm{Interactive: true, In: strings.NewReader("prod\n"), Out: &strings.Builder{}}, false, "Deploy not confirmed"},
		{"yes without CI", project.Confirm{Yes: true}, false, "Deploy not confirmed"},
		{"typed name", project.Confirm{Interactive: true, In: strings.NewReader("staging\n"), Out: &strings.Builder{}}, true, "Cannot connect"},
		{"yes in CI", project.Confirm{Yes: true, CI: true}, true, "Cannot connect"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			appendFile(t, x.root, "spinup.toml", "protected = true\n")
			x.o.Confirm = tt.confirm
			x.o.SkipBuild, x.o.Tag = true, testRelease
			x.d.Dial = func(context.Context, project.Env, bool) (remote.Runner, error) {
				x.dialed++
				return nil, errors.New("stop here")
			}
			x.expectLocalGit()
			err := x.run()
			if err == nil || !strings.Contains(err.Error(), tt.want) || (x.dialed > 0) != tt.dialed {
				t.Fatalf("error = %v, dialed = %d", err, x.dialed)
			}
		})
	}
}

func TestFlowPreflightFailures(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		setup func(*fx)
		want  string
	}{
		{"dirty tree", func(x *fx) {
			x.exec.Expect("git", "status").Return(" M api/main.go\n", "", 0)
		}, "uncommitted changes"},
		{"check problems", func(x *fx) {
			x.expectLocalGit()
			x.checkErr = errors.New("managed file differs")
		}, "spinup check found problems"},
		{"lint findings", func(x *fx) {
			x.expectLocalGit()
			x.findings = []lint.Finding{{File: "00002_more.sql", Line: 2, Rule: "drop-column", Message: "drops a column"}}
		}, "--allow-destructive"},
		{"missing secret", func(x *fx) {
			x.expectLocalGit()
			x.decrypted = map[string]string{"DATABASE_URL": "x"}
		}, "POSTGRES_PASSWORD"},
		{"unknown environment", func(x *fx) {
			x.o.Env = "nope"
		}, "Deploy preflight failed"},
		{"bad tag", func(x *fx) {
			x.expectLocalGit()
			x.o.Tag = "20261015T080000Z-aaaaaaaaaaaa"
		}, "Invalid --tag"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			tt.setup(x)
			err := x.run()
			if err == nil || !strings.Contains(err.Error()+fmtTry(err), tt.want) {
				t.Fatalf("error = %v", err)
			}
			if x.dialed != 0 || len(x.built) != 0 {
				t.Fatal("a failed preflight reached the VM or the build")
			}
		})
	}
}

func fmtTry(err error) string {
	var ue *ui.Error
	if errors.As(err, &ue) {
		return " " + ue.Why + " " + ue.Try
	}
	return ""
}

func TestFlowAllowDirtyAndDestructiveWarn(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	x.o.AllowDirty, x.o.AllowDestructive = true, true
	x.findings = []lint.Finding{{File: "00002_more.sql", Line: 2, Rule: "drop-column", Message: "drops a column"}}
	x.exec.Expect("git", "status").Return(" M x\n", "", 0)
	x.exec.Expect("git", "rev-parse", "HEAD").Return(testSHA+"\n", "", 0)
	x.d.Dial = func(context.Context, project.Env, bool) (remote.Runner, error) { return nil, errors.New("stop") }
	if err := x.run(); err == nil || !strings.Contains(err.Error(), "Cannot connect") {
		t.Fatalf("error = %v", err)
	}
	out := x.out.String()
	if !strings.Contains(out, "uncommitted changes (--allow-dirty)") || !strings.Contains(out, "00002_more.sql:2: drop-column") || !strings.Contains(out, "unsafe migration changes (--allow-destructive)") {
		t.Fatalf("output = %s", out)
	}
	if len(x.built) != 1 {
		t.Fatal("build did not run")
	}
}

func TestFlowSkipBuildFindsLatestTag(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	x.o.SkipBuild = true
	x.expectLocalGit()
	x.exec.Expect("docker", "image", "ls", "--format", "{{.Tag}}", "todo-api").Return("dev\n20261014T000000Z-0123456789ab\n"+testRelease+"\n20261016T000000Z-ffffffffffff\n", "", 0)
	x.d.Dial = func(context.Context, project.Env, bool) (remote.Runner, error) { return nil, errors.New("stop") }
	_ = x.run()
	x.exec.AssertDone(t)
	if len(x.built) != 0 || !strings.Contains(x.out.String(), "Using the images of release "+testRelease) {
		t.Fatalf("output = %s", x.out.String())
	}
}

func TestFlowVMNotBootstrapped(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	x.o.SkipBuild, x.o.Tag = true, testRelease
	x.expectLocalGit()
	x.vm.Expect("sh", "-c", probeScript).Return("", "", 3)
	x.vm.ExpectClose().Return("", "", 0)
	err := x.run()
	if err == nil || !strings.Contains(fmtTry(err), "spinup server bootstrap --env staging") {
		t.Fatalf("error = %v", err)
	}
	x.vm.AssertDone(t)
}

func TestReleaseHelpers(t *testing.T) {
	t.Parallel()
	id, err := ReleaseID(time.Date(2026, 10, 15, 10, 4, 5, 0, time.FixedZone("x", 7200)), testSHA)
	if err != nil || id != "20261015T080405Z-0123456789ab" || !ValidReleaseID(id) || !releaseTime(id).Equal(time.Date(2026, 10, 15, 8, 4, 5, 0, time.UTC)) {
		t.Fatalf("release ID = %q, %v", id, err)
	}
	if _, err := ReleaseID(testNow, "xyz"); err == nil {
		t.Fatal("invalid SHA accepted")
	}
	for _, tt := range []struct{ url, host, origin, site string }{
		{"https://todo.example.com/path", "vm", "https://todo.example.com", "todo.example.com"},
		{"http://localhost:18080", "vm", "http://localhost:18080", "http://localhost"},
		{"", "10.0.0.5", "http://10.0.0.5", "http://10.0.0.5"},
	} {
		origin, site, err := publicOrigin(tt.url, tt.host)
		if err != nil || origin != tt.origin || site != tt.site {
			t.Errorf("publicOrigin(%q) = %q %q %v", tt.url, origin, site, err)
		}
	}
	if _, _, err := publicOrigin("ftp://x", "h"); err == nil {
		t.Error("ftp accepted")
	}
	if _, err := dotEnv([][2]string{{"A", "x y"}}); err == nil {
		t.Error("unsafe .env value accepted")
	}
	kept, pruned := prependRelease([]ReleaseEntry{{Release: "a"}, {Release: "new"}, {Release: "b"}, {Release: "c"}, {Release: "d"}, {Release: "e"}}, ReleaseEntry{Release: "new"})
	if got := ids(kept); !reflect.DeepEqual(got, []string{"new", "a", "b", "c", "d"}) || !reflect.DeepEqual(ids(pruned), []string{"e"}) {
		t.Fatalf("kept %v pruned %v", got, ids(pruned))
	}
	if got := pendingMigrations([]migration{{1, "1_a.sql"}, {2, "2_b.sql"}, {10, "10_c.sql"}}, "2"); !reflect.DeepEqual(got, []string{"10_c.sql"}) {
		t.Fatalf("pending = %v", got)
	}
	if repository("ghcr.io:443/acme/app:tag") != "ghcr.io:443/acme/app" || repository("localhost:5000/app") != "localhost:5000/app" {
		t.Fatal("repository")
	}
	files := []releaseFile{{Path: "compose.yml"}, {Path: "compose.backups.yml"}, {Path: "compose.edge.yml"}, {Path: "compose.override.yml"}, {Path: "x/compose.y.yml"}}
	if got := edgeComposeFiles(files); !reflect.DeepEqual(got, []string{"compose.edge.yml", "compose.backups.yml"}) {
		t.Fatalf("edge files = %v", got)
	}
	values := redactValues(map[string]string{"A": "abc", "B": "secret-long", "C": " padded-value\n"})
	if !slices.Equal(values, []string{" padded-value\n", "padded-value", "secret-long"}) {
		t.Fatalf("redact values = %q", values)
	}
}

// TestDeployFilesWithBackups checks the release files and the base project files of an app with the backups pack.
func TestDeployFilesWithBackups(t *testing.T) {
	t.Parallel()
	x := newFx(t, true, "")
	writeTree(t, x.root, map[string]string{
		"deploy/compose.backups.yml":        "services: {}\n",
		"deploy/pgbackrest/pgbackrest.conf": "[global]\n",
		"deploy/Caddyfile.d/headers.caddy":  "header X-A b\n",
		"deploy/postgres/conf.d/extra.conf": "work_mem = 8MB\n",
		"deploy/environments/.hidden.toml":  "",
	})
	files, err := deployFiles(x.root)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	want := []string{"Caddyfile", "Caddyfile.d/headers.caddy", "Dockerfile.postgres", "compose.backups.yml", "compose.edge.yml", "compose.override.yml", "compose.yml", "pgbackrest/pgbackrest.conf", "postgres/conf.d/extra.conf", "postgres/postgresql.conf"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("release files = %v", paths)
	}
	if got := edgeComposeFiles(files); !reflect.DeepEqual(got, []string{"compose.edge.yml", "compose.backups.yml"}) {
		t.Fatalf("edge files = %v", got)
	}
	dirs := releaseDirs("/r", files)
	if !reflect.DeepEqual(dirs, []string{"/r", "/r/Caddyfile.d", "/r/pgbackrest", "/r/postgres", "/r/postgres/conf.d"}) {
		t.Fatalf("dirs = %v", dirs)
	}
	if err := os.Remove(filepath.Join(x.root, "deploy", "compose.edge.yml")); err != nil {
		t.Fatal(err)
	}
	if _, err := deployFiles(x.root); err == nil || !strings.Contains(err.Error(), "spinup restore deploy/compose.edge.yml") {
		t.Fatalf("missing edge file = %v", err)
	}
}

func TestBriefCommandErrors(t *testing.T) {
	t.Parallel()
	exit := &runexec.ExitError{Cmd: "ssh -o ... -- deploy@vm 'docker' 'compose'", Code: 1, StderrTail: "Container x Waiting\ncontainer x is unhealthy\n"}
	err := brief(fmt.Errorf("wrapped: %w", exit))
	var got *runexec.ExitError
	if err.Error() != "exit code 1: container x is unhealthy" || !errors.As(err, &got) {
		t.Fatalf("brief = %q", err)
	}
	plain := errors.New("context deadline exceeded")
	if brief(plain).Error() != plain.Error() || !errors.Is(brief(plain), plain) || brief(&runexec.ExitError{Code: 255}).Error() != "exit code 255" {
		t.Fatal("brief changed other errors")
	}
}

func ids(entries []ReleaseEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Release)
	}
	return out
}
