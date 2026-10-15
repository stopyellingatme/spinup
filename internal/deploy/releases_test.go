package deploy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestReleasesPruneKeepsCurrentAndPrevious(t *testing.T) {
	t.Parallel()
	entries := []ReleaseEntry{{Release: "a"}, {Release: "b"}, {Release: "c"}, {Release: "d"}}
	kept, removed := pruneEntries(entries, "c", 1)
	if len(kept) != 3 || kept[0].Release != "a" || kept[1].Release != "c" || kept[2].Release != "d" || len(removed) != 1 {
		t.Fatalf("kept %v, removed %v", kept, removed)
	}
}

func TestRollbackSelection(t *testing.T) {
	t.Parallel()
	entries := []ReleaseEntry{{Release: "a"}, {Release: "b"}, {Release: "c"}}
	for _, tt := range []struct {
		current, to, want string
		fail              bool
	}{{"a", "", "b", false}, {"b", "", "c", false}, {"a", "c", "c", false}, {"c", "", "", true}, {"a", "a", "", true}, {"a", "missing", "", true}} {
		got, err := rollbackTarget(entries, tt.current, tt.to)
		if (err != nil) != tt.fail || got != tt.want {
			t.Errorf("%+v: %s, %v", tt, got, err)
		}
	}
}

func TestReleasesOperations(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"list", "show", "prune"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, false, "")
			older := "20261014T080000Z-abcdef012345"
			oldest := "20261013T080000Z-abcdef012345"
			snapshot := Release{Release: testRelease, App: "todo", GitSHA: testSHA, User: "alice", CreatedAt: testNow, Secrets: SecretHashes(map[string]string{"TOKEN": "do-not-print"})}
			data, _ := json.Marshal(snapshot)
			entries := []ReleaseEntry{{Release: testRelease, GitSHA: testSHA, CreatedAt: testNow, User: "alice"}, {Release: older}, {Release: oldest}}
			history, _ := json.Marshal(entries)
			if action == "show" {
				x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRelDir+"/release.json").Return(string(data), "", 0)
			} else {
				if action == "prune" {
					x.expectLock()
				}
				x.expectReadCurrent(string(data))
				x.vm.Expect("sh", "-c", readRemoteFileScript, "spinup-read", testRoot+"/releases.json").Return(string(history), "", 0)
				if action == "prune" {
					x.vm.ExpectUpload(testRoot+"/releases.json").Return("", "", 0)
					x.ok("rm", "-rf", "--", testRoot+"/releases/"+oldest)
					x.ok("sh", "-c")
				}
			}
			x.vm.ExpectClose().Return("", "", 0)
			if err := Releases(context.Background(), ReleasesOptions{Options: x.o, Action: action, ID: testRelease, Keep: 1}, x.d); err != nil {
				t.Fatal(err)
			}
			x.vm.AssertDone(t)
			switch action {
			case "list":
				if !strings.Contains(x.out.String(), "* "+testRelease+" 0123456789ab") || !strings.Contains(x.out.String(), "alice") {
					t.Fatal(x.out.String())
				}
			case "show":
				if strings.Contains(x.out.String(), "do-not-print") || !strings.Contains(x.out.String(), snapshot.Secrets["TOKEN"]) {
					t.Fatal(x.out.String())
				}
			case "prune":
				var kept []ReleaseEntry
				uploads := x.vm.Uploads()
				if err := json.Unmarshal(uploads[len(uploads)-1].Data, &kept); err != nil || len(kept) != 2 || kept[1].Release != older {
					t.Fatal(kept, err)
				}
			}
		})
	}
}

func TestReleasesRejectsSecretValue(t *testing.T) {
	t.Parallel()
	x := newFx(t, false, "")
	snapshot := Release{Release: testRelease, Secrets: map[string]string{"TOKEN": "do-not-print"}}
	data, _ := json.Marshal(snapshot)
	x.vm.Expect("sh", "-c").Return(string(data), "", 0)
	x.vm.ExpectClose().Return("", "", 0)
	err := Releases(context.Background(), ReleasesOptions{Options: x.o, Action: "show", ID: testRelease}, x.d)
	if err == nil || strings.Contains(err.Error()+x.out.String(), "do-not-print") {
		t.Fatal(err, x.out.String())
	}
	x.vm.AssertDone(t)
}
