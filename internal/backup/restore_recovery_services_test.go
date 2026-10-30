package backup

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/deploy"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestRestoreRecoveryServices(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                               string
		web, touched, starting, startFails bool
	}{
		{name: "restart untouched"},
		{name: "print restart", startFails: true},
		{name: "changed database", touched: true},
		{name: "partial start", web: true, touched: true, starting: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vm := &remote.Fake{}
			cfg := &project.Config{Modules: []string{"jobs"}}
			names := []string{"api"}
			if tc.web {
				cfg.Modules = append(cfg.Modules, "web")
				names = append(names, "web")
			}
			names = append(names, "worker", "ocr")
			// Use the release record even when the local declaration has no services.
			rel := deploy.Release{Release: liveRelease, Color: deploy.Blue, Services: []deploy.ReleaseService{{Name: "ocr", Port: 8080, Health: "/healthz"}}}
			s := &Session{Env: Env{App: "todo", Config: cfg, Remote: vm}}
			dir := "/opt/todo/releases/" + liveRelease
			files := []string{dir + "/compose.yml", dir + "/compose.services.yml"}
			start := deploy.ComposeArgv("todo-blue", dir, files, append([]string{"up", "-d", "--no-deps", "--wait"}, names...)...)
			stop := deploy.ComposeArgv("todo-blue", dir, files, append([]string{"stop"}, names...)...)
			if !tc.touched {
				expectation := vm.Expect(start...)
				if tc.startFails {
					expectation.Return("", "failed", 1)
				}
			} else if tc.starting {
				vm.Expect(stop...)
			}
			cause := errors.New("restore failed")
			err := restoreRecovery(context.Background(), s, rel, &restoreRemote{touched: tc.touched, postgres: "running"}, tc.starting, cause)
			if !errors.Is(err, cause) {
				t.Fatalf("lost cause: %v", err)
			}
			if tc.touched || tc.startFails {
				if !strings.Contains(err.Error(), shellCommand(start)) {
					t.Fatalf("missing recovery command: %v", err)
				}
				if strings.Contains(err.Error(), "api and web") {
					t.Fatalf("incomplete status: %v", err)
				}
			}
			calls := vm.Calls()
			if len(calls) > 0 {
				want := start
				if tc.starting {
					want = stop
				}
				if len(calls) != 1 || !slices.Equal(calls[0].Argv, want) {
					t.Fatalf("calls = %v, want %v", calls, want)
				}
			}
			vm.AssertDone(t)
		})
	}
}
