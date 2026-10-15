package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func rollbackStepStart(x *fx) {
	x.ok("sh", "-c")
}

func TestRollbackSequence(t *testing.T) {
	t.Parallel()
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "previous", true: "to"}[explicit], func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			currentID := "20261015T090000Z-abcdef012345"
			current := rollbackSnapshot{Release: Release{Release: currentID, Color: Green, MigrationVersion: "2"}, PreDeployBackupTime: testNow}
			target := rollbackSnapshot{Release: Release{Release: testRelease, App: "todo", Env: "staging", GitSHA: testSHA, Color: Blue, MigrationVersion: "1", Images: []Image{{Service: "api", Ref: "todo-api:" + testRelease}}}}
			entries := []ReleaseEntry{{Release: currentID}, {Release: testRelease}}
			if explicit {
				entries = append(entries[:1], ReleaseEntry{Release: "20261015T083000Z-111111111111"}, entries[1])
			}
			currentJSON, _ := json.Marshal(current)
			targetJSON, _ := json.Marshal(target)
			history, _ := json.Marshal(entries)
			x.expectLock()
			rollbackStepStart(x)
			x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRoot+"/releases.json").Return(string(history), "", 0)
			x.expectReadCurrent(string(currentJSON))
			x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRelDir+"/release.json").Return(string(targetJSON), "", 0)
			x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRoot+"/releases/"+currentID+"/release.json").Return(string(currentJSON), "", 0)
			x.ok("sh", "-c")
			rollbackStepStart(x)
			x.expectUpstream(string(UpstreamFile(Green)))
			x.ok("sh", "-c", rollbackColorScript, "spinup-rollback", testRelDir, Blue, "todo")
			x.ok("sh", "-c")
			rollbackStepStart(x)
			x.expectSwitch(string(UpstreamFile(Green)), Blue, false)
			x.ok("sh", "-c")
			rollbackStepStart(x)
			x.http["https://todo.example.com/healthz"] = []httpCall{{status: 200}}
			x.http["https://todo.example.com/version"] = []httpCall{{status: 200, body: `{"version":"` + testRelease + `"}`}}
			x.ok("sh", "-c")
			rollbackStepStart(x)
			x.vm.ExpectUpload(testRelDir+"/release.json").Return("", "", 0)
			x.ok("ln", "-sfn", "releases/"+testRelease, testRoot+"/current")
			x.vm.ExpectUpload(testRoot+"/releases.json").Return("", "", 0)
			x.ok("sh", "-c")
			x.expectUnlockAndClose()
			o := RollbackOptions{Options: x.o}
			if explicit {
				o.To = testRelease
			}
			if err := Rollback(context.Background(), o, x.d); err != nil {
				t.Fatal(err)
			}
			x.vm.AssertDone(t)
			if !strings.Contains(x.errOut.String(), "--with-db-plan") || !strings.Contains(x.errOut.String(), testNow.Format(time.RFC3339)) {
				t.Fatal(x.errOut.String())
			}
			for _, c := range x.vm.Calls() {
				if strings.Contains(strings.Join(c.Argv, " "), "migrate") {
					t.Fatal("rollback ran a migration", c.Argv)
				}
			}
			uploads := x.vm.Uploads()
			var kept []ReleaseEntry
			if err := json.Unmarshal(uploads[len(uploads)-1].Data, &kept); err != nil || kept[0].Release != testRelease || kept[1].Release != currentID {
				t.Fatalf("history %v, %v", kept, err)
			}
		})
	}
}

func TestRollbackLockContention(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	held, _ := json.Marshal(LockInfo{Owner: "other", Acquired: testNow, TTLSeconds: 3600})
	x.vm.Expect("mkdir").Return("", "exists", 1)
	x.vm.Expect("sh", "-c").Return(string(held), "", 0)
	x.vm.ExpectClose().Return("", "", 0)
	err := Rollback(context.Background(), RollbackOptions{Options: x.o}, x.d)
	var locked *LockedError
	if !errors.As(err, &locked) {
		t.Fatal(err)
	}
	x.vm.AssertDone(t)
}

func TestRollbackMissingBackupTime(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	writeDBPlan(RollbackOptions{Options: x.o}, "todo", &rollbackSnapshot{})
	if !strings.Contains(x.errOut.String(), "no pre-deploy backup time") || strings.Contains(x.errOut.String(), "--target=") {
		t.Fatal(x.errOut.String())
	}
}

func TestRollbackProtectedEnvironment(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	appendFile(t, x.root, "spinup.toml", "protected = true\n")
	err := Rollback(context.Background(), RollbackOptions{Options: x.o}, x.d)
	if err == nil || !strings.Contains(err.Error(), "protected environment") || x.dialed != 0 {
		t.Fatal(err, x.dialed)
	}
}

func TestReleasesPruneProtectedEnvironment(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	appendFile(t, x.root, "spinup.toml", "protected = true\n")
	err := Releases(context.Background(), ReleasesOptions{Options: x.o, Action: "prune", Keep: 5}, x.d)
	if err == nil || !strings.Contains(err.Error(), "protected environment") || x.dialed != 0 {
		t.Fatal(err, x.dialed)
	}
}

func TestRollbackSwitchFailureReleasesLock(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	currentID := "20261015T090000Z-abcdef012345"
	current, _ := json.Marshal(Release{Release: currentID, Color: Green})
	target, _ := json.Marshal(Release{Release: testRelease, GitSHA: testSHA, Color: Blue})
	history, _ := json.Marshal([]ReleaseEntry{{Release: currentID}, {Release: testRelease}})
	x.expectLock()
	rollbackStepStart(x)
	x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRoot+"/releases.json").Return(string(history), "", 0)
	x.expectReadCurrent(string(current))
	x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRelDir+"/release.json").Return(string(target), "", 0)
	x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRoot+"/releases/"+currentID+"/release.json").Return(string(current), "", 0)
	x.ok("sh", "-c")
	rollbackStepStart(x)
	x.expectUpstream(string(UpstreamFile(Green)))
	x.ok("sh", "-c", rollbackColorScript, "spinup-rollback", testRelDir, Blue, "todo")
	x.ok("sh", "-c")
	rollbackStepStart(x)
	x.expectUpstream(string(UpstreamFile(Green)))
	x.vm.Expect(psArgv("todo", "caddy")...).Return("caddy1\n", "", 0)
	x.vm.Expect(x.colorArgv(Blue, "up")...).Return("", "unhealthy", 1)
	x.ok(x.colorArgv(Blue, "down", "-t", "15")...)
	x.expectUpstream(string(UpstreamFile(Green)))
	x.ok("sh", "-c", rollbackRestoreConfigScript, "spinup-rollback-restore", testRoot+"/caddy")
	x.ok("sh", "-c")
	x.expectUnlockAndClose()
	err := Rollback(context.Background(), RollbackOptions{Options: x.o}, x.d)
	if err == nil {
		t.Fatal("expected failure")
	}
	x.vm.AssertDone(t)
	for _, call := range x.vm.Calls() {
		if call.Argv[0] == "ln" {
			t.Fatal("moved current after switch failure")
		}
	}
	events := x.journalEvents()
	if events[len(events)-1] != "rollback-switch:failed" {
		t.Fatal(events)
	}
}
