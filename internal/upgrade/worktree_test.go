package upgrade

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	result, err := (exec.OSRunner{}).Run(context.Background(), exec.Cmd{Name: "git", Args: args, Dir: dir, Env: []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Spinup Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Spinup Test", "GIT_COMMITTER_EMAIL=test@example.com"}})
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, result.Stderr)
	}
	return string(result.Stdout)
}

func runFixture(t *testing.T, edit bool) (*Runner, string) {
	t.Helper()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	testGit(t, root, "init")
	testGit(t, root, "config", "user.name", "Spinup Test")
	testGit(t, root, "config", "user.email", "test@example.com")
	writePlanFile(t, root, ".gitignore", ".spinup/\n", 0644)
	writePlanFile(t, root, "file.txt", "old\n", 0644)
	lock := planLock("old\n", "scaffold")
	lock.MinCLI = "0.0.0"
	if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "initial")
	if edit {
		writePlanFile(t, root, "file.txt", "user\n", 0644)
		testGit(t, root, "commit", "-am", "user edit")
	}
	source := chainSource{"0.1.0": planVersion("0.1.0", planSpec{path: "file.txt", data: "old\n"}), "0.2.0": planVersion("0.2.0", planSpec{path: "file.txt", data: "new\n"})}
	return &Runner{Root: root, Source: source, Merger: &planMerger{}, Exec: exec.OSRunner{}, Out: &bytes.Buffer{}, ErrOut: &bytes.Buffer{}, CLIVersion: "dev"}, root
}

func TestWorktreeRun(t *testing.T) {
	t.Parallel()
	r, root := runFixture(t, false)
	if err := r.Run(context.Background(), Options{To: "0.2.0", Yes: true, NoVerify: true}); err != nil {
		t.Fatal(err)
	}
	if got := testGit(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("main dirty: %s", got)
	}
	data, _ := os.ReadFile(filepath.Join(root, "file.txt"))
	if string(data) != "old\n" {
		t.Fatalf("main changed: %s", data)
	}
	work := filepath.Join(root, ".spinup/upgrade/0.2.0")
	lock, err := manifest.Load(filepath.Join(work, "spinup.lock"))
	if err != nil || lock.TemplateVersion != "0.2.0" {
		t.Fatalf("lock=%v err=%v", lock, err)
	}
	if got := testGit(t, work, "log", "--format=%s"); !strings.Contains(got, "spinup upgrade 0.1.0 -> 0.2.0") || !strings.Contains(got, "spinup upgrade: lock 0.2.0") {
		t.Fatalf("commits: %s", got)
	}
}

func TestContinueAbort(t *testing.T) {
	t.Parallel()
	for _, abort := range []bool{false, true} {
		t.Run(map[bool]string{false: "continue", true: "abort"}[abort], func(t *testing.T) {
			t.Parallel()
			r, root := runFixture(t, true)
			ctx := context.Background()
			err := r.Run(ctx, Options{To: "0.2.0", Yes: true, NoVerify: true})
			if ui.ExitCode(err) != 3 {
				t.Fatalf("want conflicts, got %v", err)
			}
			if err := r.Run(ctx, Options{Yes: true}); err == nil || !strings.Contains(err.Error(), "--continue") {
				t.Fatalf("second run: %v", err)
			}
			if err := r.Continue(ctx, Options{Yes: true}); ui.ExitCode(err) != 3 {
				t.Fatalf("markers: %v", err)
			}
			if abort {
				r.Source = nil
				if err := r.Abort(ctx, Options{}); err == nil || !strings.Contains(err.Error(), "--yes") {
					t.Fatalf("confirmation: %v", err)
				}
				if err := r.Abort(ctx, Options{Yes: true}); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(root, ".spinup/upgrade/0.2.0")); !os.IsNotExist(err) {
					t.Fatalf("worktree exists: %v", err)
				}
				if got := testGit(t, root, "branch", "--list", "spinup/upgrade-0.2.0"); got != "" {
					t.Fatalf("branch remains: %s", got)
				}
			} else {
				writePlanFile(t, filepath.Join(root, ".spinup/upgrade/0.2.0"), "file.txt", "resolved\n", 0644)
				if err := r.Continue(ctx, Options{Yes: true, NoVerify: true}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(filepath.Join(root, ".spinup/upgrade.json")); !os.IsNotExist(err) {
				t.Fatalf("state remains: %v", err)
			}
		})
	}
}

func TestAbortMissingWorktree(t *testing.T) {
	t.Parallel()
	for _, branchGone := range []bool{false, true} {
		t.Run(map[bool]string{false: "registered", true: "branch gone"}[branchGone], func(t *testing.T) {
			t.Parallel()
			r, root := runFixture(t, true)
			ctx := context.Background()
			if err := r.Run(ctx, Options{To: "0.2.0", Yes: true, NoVerify: true}); ui.ExitCode(err) != 3 {
				t.Fatalf("expected conflict: %v", err)
			}
			work := filepath.Join(root, ".spinup/upgrade/0.2.0")
			if err := os.RemoveAll(work); err != nil {
				t.Fatal(err)
			}
			if branchGone {
				testGit(t, root, "worktree", "prune")
				testGit(t, root, "branch", "-D", "spinup/upgrade-0.2.0")
			}
			if err := r.Abort(ctx, Options{Yes: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(statePath(root)); !os.IsNotExist(err) {
				t.Fatalf("state remains: %v", err)
			}
			if got := testGit(t, root, "worktree", "list", "--porcelain"); strings.Contains(got, work) {
				t.Fatalf("registration remains: %s", got)
			}
			if got := testGit(t, root, "branch", "--list", "spinup/upgrade-0.2.0"); got != "" {
				t.Fatalf("branch remains: %s", got)
			}
		})
	}
}

func TestUpgradeStorageTrustFiles(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, tracked string
		allowUpgrade  bool
		want          bool
	}{
		{name: "trust-files", want: true},
		{name: "tracked-state", tracked: ".spinup/upgrade.json"},
		{name: "visible-worktree", allowUpgrade: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, root := runFixture(t, false)
			ignored := ".spinup/*\n!.spinup/plugins.allow\n!.spinup/plugins.history\n"
			if tc.allowUpgrade {
				ignored += "!.spinup/upgrade/\n"
			}
			writePlanFile(t, root, ".gitignore", ignored, 0644)
			for _, name := range []string{".spinup/plugins.allow", ".spinup/plugins.history"} {
				writePlanFile(t, root, name, "trust\n", 0644)
				testGit(t, root, "add", name)
			}
			if tc.tracked != "" {
				writePlanFile(t, root, tc.tracked, "state\n", 0644)
				testGit(t, root, "add", "-f", tc.tracked)
			}
			err := r.checkIgnored(t.Context(), root)
			if (err == nil) != tc.want {
				t.Fatalf("checkIgnored=%v", err)
			}
		})
	}
}
