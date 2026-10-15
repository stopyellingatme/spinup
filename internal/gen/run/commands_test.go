package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// commandGenerator plans files, the commands, and the sqlc post-step.
func commandGenerator(commands []gen.Command, files ...vfs.File) gen.Generator {
	return gen.Generator{Name: "example", Run: func(context.Context, gen.Input) (*gen.Output, error) {
		return &gen.Output{Plan: vfs.Plan{Files: files}, Commands: commands, PostSteps: []gen.PostStep{gen.PostSQLC}}, nil
	}}
}

var addDialog = []gen.Command{
	{Dir: "web", Argv: []string{"pnpm", "dlx", "shadcn-svelte@1.7.0", "add", "dialog", "--yes"}},
	{Argv: []string{"echo", "two words"}},
}

func TestCommandsRunAfterWriteBeforePostSteps(t *testing.T) {
	t.Parallel()
	root := rootForTest(t)
	fake := &exec.Fake{}
	fake.Expect("pnpm", "dlx", "shadcn-svelte@1.7.0", "add", "dialog", "--yes").Return("added dialog\n", "", 0)
	fake.Expect("echo", "two words").Return("", "", 0)
	fake.Expect("go", "tool", "-modfile=tools.mod", "sqlc", "generate").Return("", "", 0)
	var out bytes.Buffer
	g := commandGenerator(addDialog, vfs.File{Path: "web/a.txt", Data: []byte("a\n"), Class: "scaffold"})
	result, err := Run(context.Background(), g, nil, nil, Options{Root: root, Out: &out, Runner: fake})
	if err != nil {
		t.Fatal(err)
	}
	fake.AssertDone(t)
	calls := fake.Calls()
	if calls[0].Dir != filepath.Join(root, "web") || calls[1].Dir != root || calls[2].Dir != filepath.Join(root, "api") {
		t.Fatalf("dirs %q, %q, %q", calls[0].Dir, calls[1].Dir, calls[2].Dir)
	}
	if !strings.Contains(out.String(), "added dialog") || !strings.Contains(out.String(), "Created 1, updated 0 files") {
		t.Fatalf("output %q", out.String())
	}
	if want := []gen.Command{addDialog[0], {Dir: ".", Argv: addDialog[1].Argv}}; !reflect.DeepEqual(result.Commands, want) {
		t.Fatalf("commands %+v", result.Commands)
	}
}

func TestCommandsPreview(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name            string
		dry, diff, json bool
	}{{name: "dry", dry: true}, {name: "diff", diff: true}, {name: "json", dry: true, json: true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := rootForTest(t)
			var out bytes.Buffer
			fake := &exec.Fake{}
			_, err := Run(context.Background(), commandGenerator(addDialog), nil, nil, Options{Root: root, DryRun: tc.dry, Diff: tc.diff, JSON: tc.json, Out: &out, Runner: fake})
			if err != nil {
				t.Fatal(err)
			}
			if len(fake.Calls()) != 0 {
				t.Fatalf("a preview ran %v", fake.Calls())
			}
			if tc.json {
				var decoded Result
				if err := json.Unmarshal(out.Bytes(), &decoded); err != nil || len(decoded.Commands) != 2 || decoded.Commands[0].Dir != "web" {
					t.Fatalf("JSON %s, %v", out.String(), err)
				}
				return
			}
			want := "run pnpm dlx shadcn-svelte@1.7.0 add dialog --yes (in web)\nrun echo 'two words' (in .)\n"
			if out.String() != want {
				t.Fatalf("preview %q, want %q", out.String(), want)
			}
		})
	}
}

func TestCommandFailureKeepsFiles(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		expect func(*exec.Fake)
		want   string
	}{
		{"exit code", func(f *exec.Fake) { f.Expect("pnpm").Return("", "registry unreachable", 1) }, "registry unreachable"},
		{"not found", func(f *exec.Fake) { f.Expect("pnpm").Fail(&exec.NotFoundError{Name: "pnpm"}) }, `command "pnpm" was not found.`},
		{"other", func(f *exec.Fake) { f.Expect("pnpm").Fail(errors.New("boom")) }, "boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := rootForTest(t)
			fake := &exec.Fake{}
			tc.expect(fake)
			g := commandGenerator(addDialog[:1], vfs.File{Path: "web/a.txt", Data: []byte("a\n"), Class: "scaffold"})
			_, err := Run(context.Background(), g, nil, nil, Options{Root: root, Runner: fake})
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Count(err.Error(), "Try:") != 1 ||
				!strings.Contains(err.Error(), "Try: correct the problem, then run pnpm dlx shadcn-svelte@1.7.0 add dialog --yes in web by hand") {
				t.Fatalf("error %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "web", "a.txt")); err != nil {
				t.Fatalf("the written file is gone: %v", err)
			}
			lock, err := manifest.Load(filepath.Join(root, "spinup.lock"))
			if err != nil || len(lock.Files) != 1 {
				t.Fatalf("lock %+v, %v", lock, err)
			}
			// The post-steps did not run.
			fake.AssertDone(t)
		})
	}
}

func TestInvalidCommands(t *testing.T) {
	t.Parallel()
	for _, c := range []gen.Command{
		{Dir: "../web", Argv: []string{"pnpm"}},
		{Dir: "/web", Argv: []string{"pnpm"}},
		{Dir: "web/", Argv: []string{"pnpm"}},
		{Dir: "web"},
		{Dir: "web", Argv: []string{""}},
	} {
		root := rootForTest(t)
		fake := &exec.Fake{}
		_, err := Run(context.Background(), commandGenerator([]gen.Command{c}), nil, nil, Options{Root: root, Runner: fake})
		if err == nil || !strings.Contains(err.Error(), "Try: repair the generator commands") || len(fake.Calls()) != 0 {
			t.Errorf("command %+v: error %v", c, err)
		}
	}
}
