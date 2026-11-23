package deploy

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

func TestConsoleResumePostHooks(t *testing.T) {
	for _, status := range []string{"done", "failed", "started"} {
		t.Run(status, func(t *testing.T) {
			x := newFx(t, false, "")
			x.o.Resume = true
			consoleFixture(t, x, "lite")
			hookCalls := 0
			x.d.PostRelease = func() []Hook {
				return []Hook{{Name: "annotation", Run: func(context.Context, *HookContext) error {
					hookCalls++
					return nil
				}}}
			}
			x.expectLocalGit()
			x.expectConnect()
			x.expectLock()
			post, err := json.Marshal(Entry{Release: testRelease, Step: "post", Status: status, At: testNow, Detail: "earlier post result"})
			if err != nil {
				t.Fatal(err)
			}
			x.vm.Expect("sh", "-c").Return(journalUpTo(t, "monitoring")+string(post)+"\n", "", 0)
			images := []Image{{Service: "api", Ref: "todo-api:recorded"}}
			expectConsoleRead(x, images)
			x.journal(1)
			expectConsoleRead(x, images)
			expectConsoleWeb(x, false)
			expectConsoleMonitoring(x, false)
			expectConsoleRead(x, images)
			x.vm.ExpectUpload(testRoot+"/console/compose.query.yml").Return("", "", 0)
			x.ok("docker", "compose", "-p", "todo-console", "--project-directory", testRoot+"/console", "-f", testRoot+"/console/compose.console.yml", "-f", testRoot+"/console/compose.query.yml", "up", "-d", "--no-deps", "--wait", "console-query")
			x.journal(3)
			expectConsoleFinal(x, false)
			if err := x.run(); err != nil {
				t.Fatal(err)
			}
			x.vm.AssertDone(t)
			x.exec.AssertDone(t)
			wantCalls := 1
			if status == "done" {
				wantCalls = 0
			}
			if hookCalls != wantCalls {
				t.Errorf("hook calls = %d, want %d", hookCalls, wantCalls)
			}
			entries := x.journalEntries()
			last := entries[len(entries)-1]
			// ADR 0037 maps the latest post:done event to history result succeeded.
			if last.Release != testRelease || last.Step != "post" || last.Status != "done" {
				t.Fatalf("history cannot report succeeded: terminal event = %+v", last)
			}
			if status == "done" && last.Detail != "earlier post result" {
				t.Errorf("completed post detail = %q", last.Detail)
			}
			calls := x.vm.Calls()
			if len(calls) == 0 || !strings.Contains(strings.Join(calls[len(calls)-1].Argv, " "), "spinup-console-final-sample") {
				t.Fatal("resume did not request the final console sample")
			}
		})
	}
}

func TestConsoleHistoryUserAssertion(t *testing.T) {
	data, err := os.ReadFile("../../testdata/dind/console.sh")
	if err != nil {
		t.Fatal(err)
	}
	assertion := ""
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(line, "grep ") && strings.Contains(line, "^$rel1 recorded ") {
			assertion = line
			break
		}
	}
	if assertion == "" {
		t.Fatal("retained history assertion is missing")
	}
	for _, user := range []string{"runner", "", "unset"} {
		t.Run(user, func(t *testing.T) {
			work := t.TempDir()
			want := user
			if user == "" || user == "unset" {
				want = "unknown"
			}
			writeTree(t, work, map[string]string{"c-failed.history": testRelease + " recorded " + want + " " + testSHA[:12] + "\n"})
			script := "set -eu\nfail() { exit 1; }\n"
			if user == "unset" {
				script += "unset USER\n"
			}
			result, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{
				Name: "bash", Args: []string{"-c", script + assertion},
				Env: []string{"USER=" + user, "work=" + work, "rel1=" + testRelease},
			})
			if err != nil {
				t.Fatalf("history assertion with USER=%q: %v\n%s", user, err, result.Stderr)
			}
		})
	}
}
