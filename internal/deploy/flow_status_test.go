package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func statusFx(t *testing.T) (*fx, *bytes.Buffer) {
	t.Helper()
	x := newFx(t, false, "")
	var out bytes.Buffer
	x.o.Out = &out
	return x, &out
}

func TestStatus(t *testing.T) {
	t.Parallel()
	for _, asJSON := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[asJSON], func(t *testing.T) {
			t.Parallel()
			x, out := statusFx(t)
			x.o.JSON = asJSON
			current, _ := json.Marshal(Release{Release: testRelease, Color: Green, GitSHA: testSHA, User: "alice", CreatedAt: testNow})
			lock, _ := json.Marshal(LockInfo{Owner: "bob", PID: 9, Host: "ci", Acquired: testNow.Add(-time.Hour), TTLSeconds: 60, Release: "next"})
			failed, _ := json.Marshal(Entry{Release: "next", Step: "migrate", Status: "failed", At: testNow, Detail: "migrations failed\nTry: x"})
			journal := journalUpTo(t, "post") + strings.ReplaceAll(journalUpTo(t, "edge"), testRelease, "next") + string(failed) + "\n"
			x.expectUpstream(string(UpstreamFile(Green)))
			x.expectReadCurrent(string(current))
			x.vm.Expect("sh", "-c").Return(string(lock), "", 0)
			x.vm.Expect("sh", "-c").Return(journal, "", 0)
			x.vm.ExpectClose().Return("", "", 0)
			if err := Status(context.Background(), x.o, x.d); err != nil {
				t.Fatal(err)
			}
			x.vm.AssertDone(t)
			if len(x.vm.Uploads()) != 0 {
				t.Fatal("status wrote to the VM")
			}
			if asJSON {
				var r StatusReport
				if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.Current.Release != testRelease || !r.LockExpired || r.Next != "migrate" || r.Live != Green || len(r.Journal) != 13 {
					t.Fatalf("report = %#v, %v", r, err)
				}
				return
			}
			for _, want := range []string{
				"Environment: staging (vm.example.com)",
				"Current release: " + testRelease + " (green color, commit 0123456789ab, by alice at 2026-10-15T08:00:00Z)",
				"Live color: green",
				"Lock: expired, held by bob (pid 9 on ci) for release next",
				"Last deploy: next",
				"failed    migrate: migrations failed",
				"Next step: migrate",
				"Try: spinup deploy --resume --env staging",
			} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("status misses %q:\n%s", want, out.String())
				}
			}
		})
	}
}

func TestStatusEmptyVM(t *testing.T) {
	t.Parallel()
	x, out := statusFx(t)
	x.expectUpstream("")
	x.expectReadCurrent("")
	x.vm.Expect("sh", "-c").Return("", "", 3)
	x.vm.Expect("sh", "-c").Return("", "", 0)
	x.vm.ExpectClose().Return("", "", 0)
	if err := Status(context.Background(), x.o, x.d); err != nil {
		t.Fatal(err)
	}
	want := "Environment: staging (vm.example.com)\nCurrent release: none\nLive color: none\nLock: free\nLast deploy: none\n"
	if out.String() != want {
		t.Fatalf("status = %q", out.String())
	}
}

func TestUnlock(t *testing.T) {
	t.Parallel()
	x, out := statusFx(t)
	if err := Unlock(context.Background(), x.o, x.d, false); err == nil || !strings.Contains(err.Error(), "Cannot unlock") || x.dialed != 0 {
		t.Fatalf("unlock without force = %v", err)
	}
	lock, _ := json.Marshal(LockInfo{Owner: "bob", Acquired: testNow, TTLSeconds: 60, Release: "r"})
	x.vm.Expect("sh", "-c").Return(string(lock), "", 0)
	x.ok("rm", "-rf", "--", testRoot+"/deploy.lock", testRoot+"/deploy.lock.guard")
	x.vm.ExpectClose().Return("", "", 0)
	if err := Unlock(context.Background(), x.o, x.d, true); err != nil {
		t.Fatal(err)
	}
	x.vm.AssertDone(t)
	if !strings.Contains(out.String(), "held by bob for release r") {
		t.Fatalf("output = %s", out.String())
	}
}

func TestStatusDialFailure(t *testing.T) {
	t.Parallel()
	x, _ := statusFx(t)
	x.d.Dial = func(context.Context, project.Env, bool) (remote.Runner, error) {
		return nil, context.DeadlineExceeded
	}
	if err := Status(context.Background(), x.o, x.d); err == nil || !strings.Contains(err.Error(), "Cannot connect") {
		t.Fatalf("error = %v", err)
	}
}
