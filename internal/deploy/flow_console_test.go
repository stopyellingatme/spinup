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

	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func consoleFixture(t *testing.T, x *fx, profile string) {
	t.Helper()
	if profile != "absent" {
		monitoringFixture(t, x, profile, profile, true)
	}
	p := filepath.Join(x.root, "spinup.toml")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if profile == "absent" {
		data = append([]byte("modules = ['console']\n"), data...)
	} else {
		data = []byte(strings.Replace(string(data), "modules = ['monitoring']", "modules = ['console', 'monitoring']", 1))
	}
	writeTree(t, x.root, map[string]string{"spinup.toml": string(data), "deploy/Dockerfile.api": "FROM golang AS build\nRUN CGO_ENABLED=0 go build -trimpath -ldflags \"-s -w\" -o /out/console ./cmd/console\nFROM scratch\nCOPY --from=build /out/console /console\n", "console/collect.sh": "untrusted local script must never be uploaded", "console/.env": "LOCAL_SECRET=do-not-upload", "console/extra.yml": "services: {unexpected: {}}"})
	for _, name := range []string{"compose.console.yml", "compose.query.yml"} {
		data, err := os.ReadFile("../../testdata/golden/console/" + profile + "/console/" + name)
		if err != nil {
			t.Fatal(err)
		}
		writeTree(t, x.root, map[string]string{"console/" + name: string(data)})
	}
	if profile == "lite" || profile == "full" {
		x.decrypted = map[string]string{"DATABASE_URL": "db", "POSTGRES_PASSWORD": "pg", "GRAFANA_ADMIN_PASSWORD": "grafana-secret"}
	}
}

func expectConsoleRead(x *fx, images []Image) {
	data, _ := json.Marshal(Release{Release: testRelease, Color: Green, Images: images})
	x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRelDir+"/release.json").Return(string(data), "", 0)
}

func expectConsoleWeb(x *fx, fail bool) {
	stage := testRoot + "/console/.install.fixture"
	x.vm.Expect("sh", "-c").Return(stage+"\n", "", 0)
	for _, name := range []string{"compose.console.yml", ".env", "probe.conf", "collect.sh"} {
		x.vm.ExpectUpload(stage+"/"+name).Return("", "", 0)
	}
	x.ok("sh", "-c")
	up := x.vm.Expect("docker", "compose", "-p", "todo-console", "--project-directory", testRoot+"/console", "-f", testRoot+"/console/compose.console.yml", "up", "-d", "--no-deps", "--wait", "console")
	if fail {
		up.Return("", "console startup failed", 1)
	} else {
		up.Return("", "", 0)
		x.vm.Expect("docker", "compose", "-p", "todo-console", "--project-directory", testRoot+"/console", "-f", testRoot+"/console/compose.console.yml", "ps", "--all", "--quiet", "console").Return(strings.Repeat("a", 64)+"\n", "", 0)
		x.vm.Expect("docker", "inspect").Return(`{"7410/tcp":[{"HostIp":"127.0.0.1","HostPort":"7410"}]}`, "", 0)
		x.ok("sh", "-c") // No systemd user manager.
	}
	x.ok("rm", "-rf", "--", stage)
	if !fail {
		x.ok("sh", "-c", `nohup "$1" </dev/null >/dev/null 2>&1 &`, "spinup-console-sample", testRoot+"/console/collect.sh")
	}
}

func expectConsoleInactive(x *fx) {
	x.vm.ExpectUpload(testRoot+"/console/compose.query.yml").Return("", "", 0)
	x.vm.Expect("docker", "ps", "-a", "--quiet", "--no-trunc", "--filter", "label=com.docker.compose.project=todo-console", "--filter", "label=com.docker.compose.service=console-query").Return("", "", 0)
}

func TestConsoleResume(t *testing.T) {
	images := []Image{{Service: "web", Ref: "todo-web:wrong"}, {Service: "api", Ref: "todo-api:recorded"}}
	for _, last := range []string{"record", "monitoring", "post", "old journal"} {
		t.Run(last, func(t *testing.T) {
			x := newFx(t, false, "")
			x.o.Resume = true
			consoleFixture(t, x, "absent")
			x.expectLocalGit()
			x.expectConnect()
			x.expectLock()
			journal := journalUpTo(t, last)
			if last == "old journal" {
				var lines []string
				for _, line := range strings.Split(journalUpTo(t, "post"), "\n") {
					if !strings.Contains(line, `"step":"monitoring"`) {
						lines = append(lines, line)
					}
				}
				journal = strings.Join(lines, "\n")
			}
			x.vm.Expect("sh", "-c").Return(journal, "", 0)
			expectConsoleRead(x, images) // Resume restores the uploaded release.
			x.journal(1)
			expectConsoleRead(x, images)
			expectConsoleWeb(x, false)
			expectConsoleRead(x, images)
			expectConsoleInactive(x)
			x.journal(3)
			expectConsoleFinal(x, false)
			if err := x.run(); err != nil {
				t.Fatal(err)
			}
			x.vm.AssertDone(t)
			x.exec.AssertDone(t)
			want := []string{"monitoring:started", "monitoring:done", "post:started", "post:done"}
			if got := x.journalEvents(); !slices.Equal(got, want) {
				t.Fatalf("journal = %v, want %v", got, want)
			}
			env := string(x.upload(testRoot + "/console/.install.fixture/.env"))
			if !strings.Contains(env, `CONSOLE_IMAGE="todo-api:recorded"`) {
				t.Fatalf("not the recorded API image: %s", env)
			}
			script := string(x.upload(testRoot + "/console/.install.fixture/collect.sh"))
			if strings.Contains(script, "untrusted local script") || !strings.HasPrefix(script, "#!/bin/sh\n") {
				t.Fatal("uploaded local script")
			}
			for _, u := range x.vm.Uploads() {
				if strings.Contains(string(u.Data), "LOCAL_SECRET") || strings.HasSuffix(u.Path, "extra.yml") {
					t.Fatal("uploaded unselected file")
				}
			}
			calls := x.vm.Calls()
			samples := 0
			postDone := -1
			unlockIndex := -1
			for i, c := range calls {
				if len(c.Argv) == 3 && strings.Contains(c.Argv[2], "rm -rf -- '/opt/todo/deploy.lock'") {
					unlockIndex = i
				}
				if len(c.Argv) == 3 && strings.Contains(c.Argv[2], `"step":"post","status":"done"`) {
					postDone = i
				}
				if len(c.Argv) > 3 && (c.Argv[3] == "spinup-console-sample" || c.Argv[3] == "spinup-console-final-sample") {
					samples++
					if c.Argv[3] == "spinup-console-final-sample" && (unlockIndex < 0 || i < unlockIndex) {
						t.Fatal("final collection held the deploy lock")
					}
					if samples == 2 && (postDone < 0 || i < postDone) {
						t.Fatal("final sample preceded post done")
					}
				}
			}
			if samples != 2 {
				t.Fatalf("samples = %d", samples)
			}
		})
	}
}

func TestConsoleResumeRecoveryFailure(t *testing.T) {
	for _, last := range []string{"monitoring", "post"} {
		t.Run(last, func(t *testing.T) {
			x := newFx(t, false, "")
			x.o.Resume = true
			consoleFixture(t, x, "absent")
			x.d.PostRelease = func() []Hook {
				t.Error("console failure reached post hooks")
				return nil
			}
			x.expectLocalGit()
			x.expectConnect()
			x.expectLock()
			x.vm.Expect("sh", "-c").Return(journalUpTo(t, last), "", 0)
			images := []Image{{Service: "api", Ref: "todo-api:recorded"}}
			expectConsoleRead(x, images)
			x.journal(1)
			expectConsoleRead(x, images)
			expectConsoleWeb(x, true)
			x.journal(1)
			x.expectUnlockAndClose()
			if err := x.run(); err == nil || !strings.Contains(err.Error(), "start console") {
				t.Fatalf("error = %v", err)
			}
			x.vm.AssertDone(t)
			x.exec.AssertDone(t)
			want := []string{"monitoring:started", "monitoring:failed"}
			if got := x.journalEvents(); !slices.Equal(got, want) {
				t.Fatalf("journal = %v, want %v", got, want)
			}
		})
	}
}

func TestConsoleResumeCompletedRejectsOtherSHA(t *testing.T) {
	x := newFx(t, false, "")
	x.o.Resume = true
	consoleFixture(t, x, "absent")
	x.expectLocalGit()
	x.expectConnect()
	x.expectLock()
	journal := strings.ReplaceAll(journalUpTo(t, "post"), testRelease, "20261015T080000Z-aaaaaaaaaaaa")
	x.vm.Expect("sh", "-c").Return(journal, "", 0)
	x.expectUnlockAndClose()
	if err := x.run(); err == nil || !strings.Contains(err.Error(), "Cannot resume") || !strings.Contains(err.Error(), "HEAD is "+testSHA[:12]) {
		t.Fatalf("error = %v", err)
	}
	x.vm.AssertDone(t)
	x.exec.AssertDone(t)
	if len(x.journalEvents()) != 0 {
		t.Fatal("a refused resume wrote the journal")
	}
	for _, upload := range x.vm.Uploads() {
		if strings.Contains(upload.Path, "/console/") {
			t.Fatalf("a refused resume installed %s", upload.Path)
		}
	}
}

func TestConsoleResumeFailureAndRetry(t *testing.T) {
	x := newFx(t, false, "")
	x.o.Resume = true
	consoleFixture(t, x, "absent")
	x.expectLocalGit()
	x.expectConnect()
	x.expectLock()
	x.vm.Expect("sh", "-c").Return(journalUpTo(t, "record"), "", 0)
	images := []Image{{Service: "api", Ref: "todo-api:recorded"}}
	expectConsoleRead(x, images)
	x.journal(1)
	expectConsoleRead(x, images)
	expectConsoleWeb(x, true)
	x.journal(1)
	x.expectUnlockAndClose()
	err := x.run()
	if err == nil || !strings.Contains(err.Error(), "start console") || !strings.Contains(fmtTry(err), "spinup deploy --resume --env staging") {
		t.Fatalf("error = %v", err)
	}
	x.vm.AssertDone(t)
	entries := x.journalEntries()
	if entries[len(entries)-1].Step != "monitoring" || entries[len(entries)-1].Status != "failed" {
		t.Fatalf("journal = %v", entries)
	}
	// Retry the failed step with the same recorded image and existing paths.
	retry := newFx(t, false, "")
	retry.o.Resume = true
	consoleFixture(t, retry, "absent")
	retry.expectLocalGit()
	retry.expectConnect()
	retry.expectLock()
	line, _ := json.Marshal(entries[len(entries)-1])
	retry.vm.Expect("sh", "-c").Return(journalUpTo(t, "record")+string(line)+"\n", "", 0)
	expectConsoleRead(retry, images)
	retry.journal(1)
	expectConsoleRead(retry, images)
	expectConsoleWeb(retry, false)
	expectConsoleRead(retry, images)
	expectConsoleInactive(retry)
	retry.journal(3)
	expectConsoleFinal(retry, false)
	if err := retry.run(); err != nil {
		t.Fatal(err)
	}
	retry.vm.AssertDone(t)
}

func TestConsoleResumeRejectsImages(t *testing.T) {
	for _, images := range [][]Image{nil, {{Service: "web", Ref: "web:tag"}}, {{Service: "api", Tag: "api:tag"}}, {{Service: "api", Ref: "api:tag"}, {Service: "api", Ref: "api:other"}}, {{Service: "api", Ref: "api:tag;id"}}} {
		x := newFx(t, false, "")
		consoleFixture(t, x, "absent")
		l, err := loadConsole(x.root, mustConsoleConfig(t, x.root), projectEnv(), "https://todo.example.com")
		if err != nil {
			t.Fatal(err)
		}
		f := &flow{l: &local{app: "todo", console: l}, vm: x.vm, release: testRelease}
		expectConsoleRead(x, images)
		if err := f.installConsole(context.Background()); err == nil {
			t.Fatal("missing or ambiguous API image accepted")
		}
		if len(x.vm.Uploads()) != 0 {
			t.Fatal("changed VM with invalid image")
		}
		x.vm.AssertDone(t)
	}
}

func TestConsoleDeployPreflight(t *testing.T) {
	for _, name := range []string{"console/compose.console.yml", "console/compose.query.yml", "deploy/Dockerfile.api"} {
		t.Run(name, func(t *testing.T) {
			x := newFx(t, false, "")
			consoleFixture(t, x, "absent")
			if err := os.WriteFile(filepath.Join(x.root, name), []byte("# missing required instruction\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := x.run(); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("error = %v", err)
			}
			if x.dialed != 0 || len(x.built) != 0 {
				t.Fatal("invalid console reached remote changes")
			}
		})
	}
}

func TestConsolePlan(t *testing.T) {
	x := newFx(t, false, "")
	x.o.Plan = true
	consoleFixture(t, x, "absent")
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
	for _, want := range []string{"sample.lock", "recorded API image", "127.0.0.1:7410", "final sample", "exact project and service labels"} {
		if !strings.Contains(x.out.String(), want) {
			t.Fatalf("missing %s: %s", want, x.out.String())
		}
	}
	if len(x.vm.Uploads()) > 0 || len(x.journalEntries()) > 0 {
		t.Fatal("plan mutated the VM")
	}
}

func TestConsoleDeployModuleAbsence(t *testing.T) {
	x := newFx(t, false, "")
	x.expectLocalGit()
	x.d.Dial = func(context.Context, project.Env, bool) (remote.Runner, error) {
		return nil, errors.New("stop at connect")
	}
	if err := x.run(); err == nil || !strings.Contains(err.Error(), "stop at connect") {
		t.Fatalf("error = %v", err)
	}
	if len(x.built) != 1 {
		t.Fatal("normal deployment changed")
	}
}

func mustConsoleConfig(t *testing.T, root string) *project.Config {
	t.Helper()
	c, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func projectEnv() project.Env { return project.Env{Name: "staging"} }

func TestConsoleDeployStepNames(t *testing.T) {
	want := []string{"preflight", "build", "hooks", "transport", "upload", "edge", "migrate", "switch", "smoke", "record", "monitoring", "post"}
	if !reflect.DeepEqual(Steps, want) {
		t.Fatalf("journal steps changed: %v", Steps)
	}
}

func expectConsoleMonitoring(x *fx, fail bool) {
	x.ok("rm", "-rf", "--", testRoot+"/monitoring.new")
	x.ok("mkdir", "-p", "--", testRoot+"/monitoring.new", testRoot+"/monitoring.new/grafana")
	for _, name := range []string{"compose.monitoring.yml", "grafana/entrypoint.sh", ".env"} {
		x.vm.ExpectUpload(testRoot+"/monitoring.new/"+name).Return("", "", 0)
	}
	x.ok("sh", "-c", monitoringSyncScript)
	x.ok("sh", "-c", monitoringSecretsScript)
	up := x.vm.Expect("docker", "compose", "-p", "todo-monitoring")
	if fail {
		up.Return("", "monitoring startup failed", 1)
	} else {
		up.Return("", "", 0)
	}
}

func TestConsoleDeployMonitoring(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			x := newFx(t, false, "")
			x.o.Resume = true
			consoleFixture(t, x, "lite")
			x.expectLocalGit()
			x.expectConnect()
			x.expectLock()
			x.vm.Expect("sh", "-c").Return(journalUpTo(t, "record"), "", 0)
			images := []Image{{Service: "api", Ref: "todo-api:recorded"}}
			expectConsoleRead(x, images)
			x.journal(1)
			expectConsoleRead(x, images)
			expectConsoleWeb(x, false)
			expectConsoleMonitoring(x, fail)
			if fail {
				x.journal(1)
			} else {
				expectConsoleRead(x, images)
				x.vm.ExpectUpload(testRoot+"/console/compose.query.yml").Return("", "", 0)
				x.ok("docker", "compose", "-p", "todo-console", "--project-directory", testRoot+"/console", "-f", testRoot+"/console/compose.console.yml", "-f", testRoot+"/console/compose.query.yml", "up", "-d", "--no-deps", "--wait", "console-query")
				x.journal(3)
			}
			if fail {
				x.expectUnlockAndClose()
			} else {
				expectConsoleFinal(x, false)
			}
			err := x.run()
			if fail && (err == nil || !strings.Contains(err.Error(), "monitoring failed")) {
				t.Fatalf("error = %v", err)
			}
			if !fail && err != nil {
				t.Fatal(err)
			}
			x.vm.AssertDone(t)
			if len(x.upload(testRoot+"/console/.install.fixture/collect.sh")) == 0 {
				t.Fatal("console not installed before monitoring")
			}
			for _, c := range x.vm.Calls() {
				if fail && c.Argv[0] == "docker" && slices.Contains(c.Argv, "console-query") {
					t.Fatal("relay started after failed monitoring")
				}
			}
		})
	}
}

func TestConsoleDeployFinalSampleFailure(t *testing.T) {
	x := newFx(t, false, "")
	x.o.Resume = true
	consoleFixture(t, x, "absent")
	x.expectLocalGit()
	x.expectConnect()
	x.expectLock()
	x.vm.Expect("sh", "-c").Return(journalUpTo(t, "record"), "", 0)
	images := []Image{{Service: "api", Ref: "todo-api:recorded"}}
	expectConsoleRead(x, images)
	x.journal(1)
	expectConsoleRead(x, images)
	expectConsoleWeb(x, false)
	expectConsoleRead(x, images)
	expectConsoleInactive(x)
	x.journal(3)
	expectConsoleFinal(x, true)
	if err := x.run(); err != nil {
		t.Fatal(err)
	}
	x.vm.AssertDone(t)
	if !strings.Contains(x.errOut.String(), "Console sample delayed. Try: spinup status --env staging") {
		t.Fatalf("warning = %s", x.errOut.String())
	}
	entries := x.journalEntries()
	last := entries[len(entries)-1]
	if last.Step != "post" || last.Status != "done" {
		t.Fatalf("successful deployment lost: %v", last)
	}
}

func TestConsoleDeploy(t *testing.T) {
	x := newFx(t, false, "")
	consoleFixture(t, x, "absent")
	x.expectLocalGit()
	x.expectConnect()
	x.expectLock()
	x.expectBegin()
	x.journal(1)
	x.expectReadCurrent("")
	x.journal(1)
	x.journal(1)
	x.exec.Expect("docker", "image", "inspect", "--format", "{{.Id}} "+imageFingerprint, "todo-api:"+testRelease).Return("sha256:aaa linux/amd64 [\"sha256:l1\"]\n", "", 0)
	x.vm.Expect("docker", "image", "inspect", "--format", imageFingerprint, "todo-api:"+testRelease).Return("", "No such image", 1)
	x.exec.Expect("docker", "save", "-o").Return("", "", 0)
	x.ok("docker", "load")
	x.vm.Expect("docker", "image", "inspect", "--format", imageFingerprint, "todo-api:"+testRelease).Return("linux/amd64 [\"sha256:l1\"]\n", "", 0)
	x.journal(1)
	x.journal(1)
	x.expectUploadRelease("")
	x.journal(1)
	x.journal(1)
	x.expectEdge("", "")
	x.journal(1)
	x.journal(1)
	x.expectMigrate()
	x.journal(1)
	x.journal(1)
	x.expectSwitch(string(PlaceholderUpstream), Blue, false)
	x.journal(1)
	x.journal(1)
	x.expectSmoke(Blue)
	x.journal(1)
	x.journal(1)
	x.expectRecord("", testRelease+"\n")
	x.journal(1)
	x.journal(1)
	images := []Image{{Service: "api", Ref: "todo-api:" + testRelease}}
	expectConsoleRead(x, images)
	expectConsoleWeb(x, false)
	expectConsoleRead(x, images)
	expectConsoleInactive(x)
	x.journal(3)
	expectConsoleFinal(x, false)
	if err := x.run(); err != nil {
		t.Fatal(err)
	}
	x.vm.AssertDone(t)
	x.exec.AssertDone(t)
	if !reflect.DeepEqual(x.journalEvents(), allSteps) {
		t.Fatalf("normal journal order changed: %v", x.journalEvents())
	}
	x.assertNoSecrets()
}

func expectConsoleFinal(x *fx, fail bool) {
	x.ok("sh", "-c") // Release the deploy lock before collection.
	call := x.vm.Expect("sh", "-c")
	if fail {
		call.Return("", "projector failed", 1)
	} else {
		call.Return("", "", 0)
	}
	x.vm.ExpectClose().Return("", "", 0)
}
