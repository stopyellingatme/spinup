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

	"github.com/spf13/pflag"
	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

func rootForTest(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := manifest.New().Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	return root
}
func put(t *testing.T, root, name, data string) {
	t.Helper()
	filename := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}
func generator(files ...vfs.File) gen.Generator {
	return gen.Generator{Name: "example", Run: func(context.Context, gen.Input) (*gen.Output, error) {
		return &gen.Output{Plan: vfs.Plan{Files: files}, Next: []string{"spinup test"}}, nil
	}}
}
func TestRunActions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name            string
		dry, diff, json bool
	}{
		{name: "apply"}, {name: "dry", dry: true}, {name: "diff", diff: true}, {name: "json", json: true}, {name: "json-preview", dry: true, json: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := rootForTest(t)
			put(t, root, "api/existing.txt", "old\n")
			put(t, root, "api/same.txt", "same\n")
			before, err := os.ReadFile(filepath.Join(root, "spinup.lock"))
			if err != nil {
				t.Fatal(err)
			}
			g := generator(vfs.File{Path: "api/new.txt", Data: []byte("new\n"), Class: "scaffold", Mode: 0755}, vfs.File{Path: "api/existing.txt", Data: []byte("updated\n"), Class: "seed"}, vfs.File{Path: "api/same.txt", Data: []byte("same\n"), Class: "managed", Mode: 0755})
			var out bytes.Buffer
			fake := &exec.Fake{}
			result, err := Run(context.Background(), g, []string{"arg"}, pflag.NewFlagSet("example", pflag.ContinueOnError), Options{Root: root, DryRun: tc.dry, Diff: tc.diff, JSON: tc.json, Force: true, Out: &out, Runner: fake})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Files) != 3 || result.Files[0].Action != "update" || result.Files[1].Action != "create" || result.Files[2].Action != "unchanged" {
				t.Fatalf("result=%+v", result)
			}
			switch {
			case tc.json:
				var decoded Result
				if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(result, &decoded) {
					t.Fatalf("JSON=%s", out.String())
				}
			case tc.diff:
				if !strings.Contains(out.String(), "--- a/api/existing.txt\n+++ b/api/existing.txt") || !strings.Contains(out.String(), "+updated") {
					t.Fatalf("diff=%s", out.String())
				}
			case tc.dry:
				if !strings.Contains(out.String(), "create api/new.txt (scaffold)") {
					t.Fatal(out.String())
				}
			default:
				if !strings.Contains(out.String(), "Created 1, updated 1 files\nNext: spinup test\n") {
					t.Fatal(out.String())
				}
			}
			fake.AssertDone(t)
			lock, err := manifest.Load(filepath.Join(root, "spinup.lock"))
			if err != nil {
				t.Fatal(err)
			}
			if tc.dry || tc.diff {
				after, _ := os.ReadFile(filepath.Join(root, "spinup.lock"))
				if !bytes.Equal(before, after) {
					t.Fatal("preview changed lock")
				}
				if _, err := os.Stat(filepath.Join(root, "api/new.txt")); !os.IsNotExist(err) {
					t.Fatal("preview wrote new file")
				}
				data, _ := os.ReadFile(filepath.Join(root, "api/existing.txt"))
				if string(data) != "old\n" {
					t.Fatal("preview changed existing file")
				}
			} else {
				entry := lock.Files["api/new.txt"]
				if entry.Class != manifest.Scaffold || entry.Template != "gen/example" || entry.Hash != manifest.Hash([]byte("new\n")) || entry.Mode != "0755" {
					t.Fatalf("entry=%+v", entry)
				}
				if len(lock.Files) != 2 {
					t.Fatalf("identical file was recorded: %+v", lock.Files)
				}
				info, _ := os.Stat(filepath.Join(root, "api/same.txt"))
				if info.Mode().Perm() != 0644 {
					t.Fatal("identical content changed mode")
				}
			}
		})
	}
}

func TestOverwrite(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, class, template string
		force, allow          bool
	}{
		{name: "untracked", class: "managed"}, {name: "scaffold", class: "scaffold", template: "gen/example"},
		{name: "pack-owned", class: "managed", template: "api/file.tmpl"}, {name: "generator-owned", class: "managed", template: "gen/other", allow: true},
		{name: "force", class: "scaffold", force: true, allow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := rootForTest(t)
			put(t, root, "api/file.txt", "old")
			if tc.template != "" {
				lock, _ := manifest.Load(filepath.Join(root, "spinup.lock"))
				if err := lock.SetFile("api/file.txt", manifest.FileEntry{Class: manifest.Managed, Template: tc.template, Hash: manifest.Hash([]byte("old")), Mode: "0644"}); err != nil {
					t.Fatal(err)
				}
				if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
					t.Fatal(err)
				}
			}
			_, err := Run(context.Background(), generator(vfs.File{Path: "api/file.txt", Data: []byte("new"), Class: tc.class}), nil, nil, Options{Root: root, Force: tc.force})
			if tc.allow {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "api/file.txt") || !strings.HasSuffix(err.Error(), "Try: pass --force, or remove the files") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestInvalidPlans(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		files []vfs.File
	}{
		{"escape", []vfs.File{{Path: "../escape", Class: "managed"}}}, {"absolute", []vfs.File{{Path: "/escape", Class: "managed"}}},
		{"unclean", []vfs.File{{Path: "api//x", Class: "managed"}}}, {"class", []vfs.File{{Path: "api/x"}}},
		{"unknown-class", []vfs.File{{Path: "api/x", Class: "other"}}}, {"duplicate", []vfs.File{{Path: "api/x", Class: "managed"}, {Path: "api/X", Class: "managed"}}},
		{"delete", []vfs.File{{Path: "api/x", Class: "managed", Delete: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := rootForTest(t)
			if _, err := Run(context.Background(), generator(tc.files...), nil, nil, Options{Root: root, Force: true}); err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("err=%v", err)
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		t.Parallel()
		root := rootForTest(t)
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "api")); err != nil {
			t.Fatal(err)
		}
		if _, err := Run(context.Background(), generator(vfs.File{Path: "api/x", Class: "managed"}), nil, nil, Options{Root: root, Force: true}); err == nil {
			t.Fatal("accepted outside symlink")
		}
	})
}

func TestPostSteps(t *testing.T) {
	stepsMu.Lock()
	original := steps[gen.PostOpenAPI]
	delete(steps, gen.PostOpenAPI)
	stepsMu.Unlock()
	t.Cleanup(func() {
		stepsMu.Lock()
		defer stepsMu.Unlock()
		if original == nil {
			delete(steps, gen.PostOpenAPI)
		} else {
			steps[gen.PostOpenAPI] = original
		}
	})
	// Register once before parallel cases. Other runner tests do not request this step.
	RegisterPostStep(gen.PostOpenAPI, func(ctx context.Context, root string, files []string, r exec.Runner) error {
		return tool(ctx, root, r, "openapi-test", files...)
	})
	for _, tc := range []struct {
		name            string
		dry, diff, fail bool
	}{{name: "order"}, {name: "dry", dry: true}, {name: "diff", diff: true}, {name: "failure", fail: true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := rootForTest(t)
			fake := &exec.Fake{}
			if !tc.dry && !tc.diff {
				fake.Expect("openapi-test").Return("bundle output", "", 0)
				code := 0
				if tc.fail {
					code = 1
				}
				fake.Expect("go", "tool", "-modfile=tools.mod", "sqlc", "generate").Return("tool stdout", "tool stderr", code)
				if !tc.fail {
					fake.Expect("gofmt", "-w", "internal/x.go").Return("", "", 0)
				}
			}
			g := generator(vfs.File{Path: "api/internal/x.go", Data: []byte("package x\n"), Class: "scaffold"}, vfs.File{Path: "api/q.sql", Class: "scaffold"})
			orig := g.Run
			g.Run = func(ctx context.Context, in gen.Input) (*gen.Output, error) {
				out, err := orig(ctx, in)
				out.PostSteps = []gen.PostStep{gen.PostGofmt, gen.PostSQLC, gen.PostOpenAPI, gen.PostSQLC}
				return out, err
			}
			result, err := Run(context.Background(), g, nil, nil, Options{Root: root, DryRun: tc.dry, Diff: tc.diff, Runner: fake})
			if tc.fail {
				if err == nil || !strings.Contains(err.Error(), "sqlc") || !strings.Contains(err.Error(), "tool stdout") || !strings.Contains(err.Error(), "tool stderr") || !strings.Contains(err.Error(), "Try:") {
					t.Fatalf("err=%v", err)
				}
				if _, err := os.Stat(filepath.Join(root, "api/internal/x.go")); err != nil {
					t.Fatal("failed tool removed files")
				}
				lock, err := manifest.Load(filepath.Join(root, "spinup.lock"))
				if err != nil || len(lock.Files) != 2 {
					t.Fatalf("lock=%+v, %v", lock, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if result == nil || !reflect.DeepEqual(result.PostSteps, []gen.PostStep{gen.PostOpenAPI, gen.PostSQLC, gen.PostGofmt}) {
				t.Fatalf("result=%+v", result)
			}
			for _, call := range fake.Calls() {
				if call.Dir != filepath.Join(root, "api") {
					t.Fatalf("Dir=%s", call.Dir)
				}
			}
			fake.AssertDone(t)
		})
	}
}

func TestRunErrorsAndInput(t *testing.T) {
	t.Parallel()
	t.Run("input", func(t *testing.T) {
		t.Parallel()
		root := rootForTest(t)
		flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
		g := generator()
		g.Run = func(_ context.Context, in gen.Input) (*gen.Output, error) {
			if in.Root != root || in.Lock == nil || in.Flags != flags || !reflect.DeepEqual(in.Args, []string{"a"}) {
				t.Fatalf("input=%+v", in)
			}
			return &gen.Output{}, nil
		}
		if _, err := Run(context.Background(), g, []string{"a"}, flags, Options{Root: root}); err != nil {
			t.Fatal(err)
		}
	})
	for _, name := range []string{"missing-lock", "nil-output", "generator-error", "missing-step"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := rootForTest(t)
			g := generator()
			switch name {
			case "missing-lock":
				if err := os.Remove(filepath.Join(root, "spinup.lock")); err != nil {
					t.Fatal(err)
				}
			case "nil-output":
				g.Run = func(context.Context, gen.Input) (*gen.Output, error) { return nil, nil }
			case "generator-error":
				g.Run = func(context.Context, gen.Input) (*gen.Output, error) { return nil, errors.New("bad argument") }
			case "missing-step":
				g.Run = func(context.Context, gen.Input) (*gen.Output, error) {
					return &gen.Output{PostSteps: []gen.PostStep{"unknown"}}, nil
				}
			}
			if _, err := Run(context.Background(), g, nil, nil, Options{Root: root}); err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestNoOpAndConflictList(t *testing.T) {
	t.Parallel()
	t.Run("identical", func(t *testing.T) {
		t.Parallel()
		root := rootForTest(t)
		put(t, root, "api/same.txt", "same")
		before, err := os.ReadFile(filepath.Join(root, "spinup.lock"))
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		result, err := Run(context.Background(), generator(vfs.File{Path: "api/same.txt", Data: []byte("same"), Class: "scaffold"}), nil, nil, Options{Root: root, Out: &out})
		if err != nil {
			t.Fatal(err)
		}
		if result.Files[0].Action != "unchanged" || !strings.Contains(out.String(), "Created 0, updated 0 files") {
			t.Fatalf("result=%+v output=%s", result, out.String())
		}
		after, err := os.ReadFile(filepath.Join(root, "spinup.lock"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("no-op changed lock")
		}
	})
	t.Run("all-conflicts", func(t *testing.T) {
		t.Parallel()
		root := rootForTest(t)
		put(t, root, "api/a", "old")
		put(t, root, "api/z", "old")
		_, err := Run(context.Background(), generator(vfs.File{Path: "api/z", Data: []byte("new"), Class: "scaffold"}, vfs.File{Path: "api/a", Data: []byte("new"), Class: "seed"}, vfs.File{Path: "api/new", Class: "managed"}), nil, nil, Options{Root: root})
		if err == nil || !strings.Contains(err.Error(), "api/a\n  api/z") {
			t.Fatalf("err=%v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "api/new")); !os.IsNotExist(err) {
			t.Fatal("conflict partially wrote plan")
		}
	})
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("output closed") }

func TestOutputFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name            string
		dry, diff, json bool
	}{
		{name: "summary"}, {name: "preview", dry: true}, {name: "diff", diff: true}, {name: "JSON", json: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Run(context.Background(), generator(vfs.File{Path: "api/new", Data: []byte("new"), Class: "scaffold"}), nil, nil, Options{Root: rootForTest(t), DryRun: tc.dry, Diff: tc.diff, JSON: tc.json, Out: failingWriter{}})
			if err == nil || !strings.Contains(err.Error(), "output closed") || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestGofmtPostStep(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path string
		fail       bool
	}{
		{name: "no-go-files", path: "api/queries.sql"},
		{name: "failure", path: "api/internal/x.go", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := rootForTest(t)
			fake := &exec.Fake{}
			if tc.fail {
				fake.Expect("gofmt", "-w", "internal/x.go").Return("", "invalid Go syntax", 1)
			}
			g := generator()
			g.Run = func(context.Context, gen.Input) (*gen.Output, error) {
				return &gen.Output{Plan: vfs.Plan{Files: []vfs.File{{Path: tc.path, Data: []byte("content"), Class: "scaffold"}}}, PostSteps: []gen.PostStep{gen.PostGofmt}}, nil
			}
			_, err := Run(context.Background(), g, nil, nil, Options{Root: root, Runner: fake})
			if tc.fail {
				if err == nil || !strings.Contains(err.Error(), "invalid Go syntax") || !strings.HasSuffix(err.Error(), "Try: run gofmt -w internal/x.go in api/ by hand") {
					t.Fatalf("err=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			fake.AssertDone(t)
		})
	}
}

func TestMissingPostStepKeepsFiles(t *testing.T) {
	t.Parallel()
	root := rootForTest(t)
	g := generator()
	g.Run = func(context.Context, gen.Input) (*gen.Output, error) {
		return &gen.Output{Plan: vfs.Plan{Files: []vfs.File{{Path: "api/new.txt", Data: []byte("new"), Class: "scaffold"}}}, PostSteps: []gen.PostStep{"unregistered"}}, nil
	}
	_, err := Run(context.Background(), g, nil, nil, Options{Root: root})
	if err == nil || !strings.Contains(err.Error(), "unregistered") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("err=%v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "api/new.txt"))
	if err != nil || string(data) != "new" {
		t.Fatalf("data=%s err=%v", data, err)
	}
	lock, err := manifest.Load(filepath.Join(root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Files["api/new.txt"].Template != "gen/example" {
		t.Fatalf("lock=%+v", lock.Files)
	}
}
