package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

func TestReleaseChecks(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, kit, want string
		tagged          bool
		prepare         func(*testing.T, string)
	}{
		{name: "first release"},
		{name: "invalid version", kit: "bad", want: "invalid version"},
		{name: "dirty tree", want: "not clean", prepare: func(t *testing.T, root string) { write(t, root, "dirty", "changed") }},
		{name: "missing section", want: "no section", prepare: func(t *testing.T, root string) { write(t, root, "CHANGELOG.md", "## [Unreleased]\n"); commit(t, root) }},
		{name: "existing tag", want: "already exists", prepare: func(t *testing.T, root string) { gitTest(t, root, "tag", "v0.8.0-rc.1") }},
		{name: "missing workflow tag", tagged: true, want: "is missing"},
		{name: "workflow tag", tagged: true, prepare: func(t *testing.T, root string) { gitTest(t, root, "tag", "v0.8.0-rc.1") }},
		{name: "wrong workflow commit", tagged: true, want: "does not identify HEAD", prepare: func(t *testing.T, root string) {
			gitTest(t, root, "tag", "v0.8.0-rc.1")
			write(t, root, "next", "new")
			commit(t, root)
		}},
		{name: "existing kit tag", want: "already exists", prepare: func(t *testing.T, root string) { gitTest(t, root, "tag", "kit/v0.8.0-rc.1") }},
		{name: "workflow retry", tagged: true, prepare: func(t *testing.T, root string) {
			gitTest(t, root, "tag", "v0.8.0-rc.1")
			gitTest(t, root, "tag", "kit/v0.8.0-rc.1")
		}},
		{name: "changed kit mismatched minor", kit: "0.7.3", want: "major and minor", prepare: changedKit},
		{name: "changed kit independent patch", kit: "0.8.3", prepare: changedKit},
		{name: "unchanged kit independent release", kit: "0.7.3", prepare: func(t *testing.T, root string) { gitTest(t, root, "tag", "kit/v0.7.0") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			gitTest(t, root, "init", "-q")
			gitTest(t, root, "config", "user.email", "test@example.test")
			gitTest(t, root, "config", "user.name", "Release Test")
			write(t, root, "CHANGELOG.md", "## [0.8.0-rc.1] - 2026-10-15\n")
			write(t, root, "kit/file", "kit")
			commit(t, root)
			if test.prepare != nil {
				test.prepare(t, root)
			}
			kit := test.kit
			if kit == "" {
				kit = "0.8.0-rc.1"
			}
			err := check(root, "0.8.0-rc.1", kit, true, test.tagged)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %s", err, test.want)
			}
		})
	}
}

func changedKit(t *testing.T, root string) {
	t.Helper()
	gitTest(t, root, "tag", "kit/v0.7.0")
	write(t, root, "kit/file", "new kit")
	commit(t, root)
}

func write(t *testing.T, root, name, content string) {
	t.Helper()
	name = filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, root string) {
	t.Helper()
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-qm", "fixture")
}

func gitTest(t *testing.T, root string, args ...string) {
	t.Helper()
	if _, err := (runexec.OSRunner{}).Run(context.Background(), runexec.Cmd{Name: "git", Args: args, Dir: root, Env: []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}}); err != nil {
		t.Fatal(err)
	}
}
