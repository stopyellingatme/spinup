package pack

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	execx "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/plugin"
)

func manager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte("# keep this\n[app]\nname = 'test' # inline\nmodule = 'example.com/test'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := manifest.New().Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	return &Manager{Root: root, CacheDir: t.TempDir(), CLIVersion: "1.2.3", Trust: true, Out: &bytes.Buffer{}}
}

func TestHistoricalCacheIsolation(t *testing.T) {
	t.Parallel()
	for _, approvedOld := range []bool{false, true} {
		t.Run(fmt.Sprintf("approved-old-%t", approvedOld), func(t *testing.T) {
			t.Parallel()
			a, b := manager(t), manager(t)
			b.CacheDir = a.CacheDir
			const source = "path:./plugins/foo"
			for _, m := range []*Manager{a, b} {
				if err := os.CopyFS(filepath.Join(m.Root, "plugins/foo"), os.DirFS(fixture(t))); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.Add(t.Context(), source, ""); err != nil {
				t.Fatal(err)
			}
			oldA, err := a.resolve(t.Context(), plugin.Options{Root: a.Root})
			if err != nil {
				t.Fatal(err)
			}
			bSource := filepath.Join(b.Root, "plugins/foo")
			if err := os.WriteFile(filepath.Join(bSource, "files/example.txt.tmpl"), []byte("project B\n"), 0644); err != nil {
				t.Fatal(err)
			}
			var oldHash string
			if approvedOld {
				if err := b.Add(t.Context(), source, ""); err != nil {
					t.Fatal(err)
				}
				old, err := b.resolve(t.Context(), plugin.Options{Root: b.Root})
				if err != nil {
					t.Fatal(err)
				}
				oldHash = old[0].SHA256
			}
			replaceLine(t, bSource+"/plugin.toml", "version", "version = '2.0.0'")
			if approvedOld {
				if err := b.Update(t.Context(), "example", "2.0.0"); err != nil {
					t.Fatal(err)
				}
			} else if err := b.Add(t.Context(), source, ""); err != nil {
				t.Fatal(err)
			}
			// A refresh must never replace B's previously approved tree.
			if err := a.Verify(t.Context()); err != nil {
				t.Fatal(err)
			}
			got, err := b.resolve(t.Context(), plugin.Options{Root: b.Root, Versions: map[string]string{"example": "1.0.0"}})
			if !approvedOld {
				if err == nil || !strings.Contains(err.Error(), "Try:") {
					t.Fatalf("unapproved OLD=%v error=%v", got, err)
				}
				return
			}
			if err != nil || len(got) != 1 || got[0].SHA256 != oldHash || got[0].SHA256 == oldA[0].SHA256 {
				t.Fatalf("OLD=%v error=%v", got, err)
			}
		})
	}
}

type fetchRunnerFunc func(context.Context, execx.Cmd) (execx.Result, error)

func (f fetchRunnerFunc) Run(ctx context.Context, cmd execx.Cmd) (execx.Result, error) {
	return f(ctx, cmd)
}

func TestGitFetchCancelsOversizedClone(t *testing.T) {
	t.Parallel()
	m := manager(t)
	var temp string
	m.Runner = fetchRunnerFunc(func(ctx context.Context, cmd execx.Cmd) (execx.Result, error) {
		temp = cmd.Dir
		f, err := os.Create(filepath.Join(cmd.Dir, "oversized-pack"))
		if err != nil {
			return execx.Result{}, err
		}
		err = f.Truncate(maxFetchBytes + 1)
		closeErr := f.Close()
		if err != nil {
			return execx.Result{}, err
		}
		if closeErr != nil {
			return execx.Result{}, closeErr
		}
		<-ctx.Done()
		return execx.Result{}, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := m.fetch(ctx, "git:https://example.com/repo", "v1.0.0", "example")
	if err == nil || !strings.Contains(err.Error(), "fetch exceeds") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("oversized clone=%v", err)
	}
	if _, err := os.Stat(temp); !os.IsNotExist(err) {
		t.Fatalf("fetch directory remains: %v", err)
	}
}

func TestGitFetchChecksCompletedClone(t *testing.T) {
	t.Parallel()
	m := manager(t)
	m.Runner = fetchRunnerFunc(func(_ context.Context, cmd execx.Cmd) (execx.Result, error) {
		f, err := os.Create(filepath.Join(cmd.Dir, "oversized-pack"))
		if err != nil {
			return execx.Result{}, err
		}
		truncateErr := f.Truncate(maxFetchBytes + 1)
		closeErr := f.Close()
		if truncateErr != nil {
			return execx.Result{}, truncateErr
		}
		return execx.Result{}, closeErr
	})
	if _, err := m.fetch(t.Context(), "git:https://example.com/repo", "v1.0.0", "example"); err == nil || !strings.Contains(err.Error(), "fetch exceeds") {
		t.Fatalf("completed oversized clone=%v", err)
	}
}

func TestTrustLifecycle(t *testing.T) {
	t.Parallel()
	m := manager(t)
	source := "path:" + fixture(t)
	if err := m.Add(t.Context(), source, ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(m.Root, "spinup.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("# keep this")) || !bytes.Contains(data, []byte("# inline")) {
		t.Fatal("comments lost")
	}
	if _, err := os.Stat(filepath.Join(m.Root, "example.txt")); !os.IsNotExist(err) {
		t.Fatal("add rendered files")
	}
	if _, err := m.resolve(t.Context(), plugin.Options{Root: m.Root}); err != nil {
		t.Fatal(err)
	}
	if err := m.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	m.Offline = true
	if _, err := m.resolve(t.Context(), plugin.Options{Root: m.Root, Offline: true}); err != nil {
		t.Fatal(err)
	}
	m.Offline = false
	if err := os.WriteFile(filepath.Join(strings.TrimPrefix(source, "path:"), "files/example.txt.tmpl"), []byte("changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := m.Verify(t.Context()); err == nil {
		t.Fatal("changed source accepted")
	}
	if err := m.Update(t.Context(), "example", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(t.Context(), "example"); err != nil {
		t.Fatal(err)
	}
	loaded, err := m.resolve(t.Context(), plugin.Options{Root: m.Root})
	if err != nil || loaded != nil {
		t.Fatalf("empty resolve = %v, %v", loaded, err)
	}
}

func TestResolveFailures(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"missing-allowlist", "bad-allowlist", "bad-lock", "offline-miss"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			m := manager(t)
			if err := m.Add(t.Context(), "path:"+fixture(t), ""); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing-allowlist":
				if err := os.Remove(filepath.Join(m.Root, plugin.AllowlistFile)); err != nil {
					t.Fatal(err)
				}
			case "bad-allowlist":
				if err := os.WriteFile(filepath.Join(m.Root, plugin.AllowlistFile), []byte("example path:bad "+strings.Repeat("0", 64)+"\n"), 0644); err != nil {
					t.Fatal(err)
				}
			case "bad-lock":
				l, err := manifest.Load(filepath.Join(m.Root, "spinup.lock"))
				if err != nil {
					t.Fatal(err)
				}
				l.Plugins[0].SHA256 = strings.Repeat("0", 64)
				if err := l.Save(filepath.Join(m.Root, "spinup.lock")); err != nil {
					t.Fatal(err)
				}
			case "offline-miss":
				m.CacheDir = t.TempDir()
			}
			_, err := m.resolve(t.Context(), plugin.Options{Root: m.Root, Offline: kind == "offline-miss"})
			if err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestGitCommand(t *testing.T) {
	t.Parallel()
	m := manager(t)
	fake := &execx.Fake{}
	m.Runner = fake
	fake.Expect("git", "-c", "protocol.file.allow=never").Return("", "stop", 1)
	_, err := m.fetch(context.Background(), "git:https://example.com/repo", "v1.0.0", "")
	if err == nil {
		t.Fatal("expected fake failure")
	}
	fake.AssertDone(t)
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatal(calls)
	}
	call := calls[0]
	args := strings.Join(call.Args, " ")
	for _, want := range []string{"--depth 1", "--branch v1.0.0", "--no-recurse-submodules", "-- https://example.com/repo"} {
		if !strings.Contains(args, want) {
			t.Errorf("argv lacks %q: %s", want, args)
		}
	}
	if call.Timeout <= 0 {
		t.Fatal("missing timeout")
	}
	for _, source := range []string{"git:file:///tmp/repo", "git:/tmp/repo", "git:--upload-pack=evil"} {
		if _, err := m.fetch(t.Context(), source, "v1", ""); err == nil {
			t.Fatalf("accepted %s", source)
		}
	}
}

func TestVersionOverrideAndCacheTamper(t *testing.T) {
	t.Parallel()
	m := manager(t)
	root := fixture(t)
	if err := m.Add(t.Context(), "path:"+root, ""); err != nil {
		t.Fatal(err)
	}
	replaceLine(t, root+"/plugin.toml", "version", "version = '2.0.0'")
	if err := m.Update(t.Context(), "example", "v2.0.0"); err != nil {
		t.Fatal(err)
	}
	loaded, err := m.resolve(t.Context(), plugin.Options{Root: m.Root, Versions: map[string]string{"example": "1.0.0"}})
	if err != nil || len(loaded) != 1 || loaded[0].Meta.Version != "1.0.0" {
		t.Fatalf("override = %v, %v", loaded, err)
	}
	m.Offline = true
	if _, err := m.resolve(t.Context(), plugin.Options{Root: m.Root, Offline: true}); err != nil {
		t.Fatal(err)
	}
	lock, err := manifest.Load(filepath.Join(m.Root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	dir, err := m.cacheDir("example", "v2.0.0", "path:"+root, lock.Plugins[0].SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tree", "files", "example.txt.tmpl"), []byte("tamper\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := m.Verify(t.Context()); err == nil || !strings.Contains(err.Error(), "hash differs") {
		t.Fatalf("tampered cache = %v", err)
	}
}

func TestRecordsPreserveCommentsAndOwnedFiles(t *testing.T) {
	t.Parallel()
	m := manager(t)
	first := fixture(t)
	second := fixture(t)
	replaceLine(t, second+"/plugin.toml", "name", "name = 'second'")
	replaceLine(t, second+"/pack.toml", "name", "name = 'second'")
	for _, source := range []string{first, second} {
		if err := m.Add(t.Context(), "path:"+source, ""); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(m.Root, "spinup.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("name = 'example'"), []byte("name = 'example' # keep plugin comment"), 1)
	data = append(data, []byte("\n[env.test]\n# environment comment\nhost = 'localhost'\n")...)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	lock, err := manifest.Load(filepath.Join(m.Root, "spinup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	lock.Files["keep.txt"] = manifest.FileEntry{Class: manifest.Managed, Template: "plugin/example/files/keep.txt.tmpl", Hash: manifest.Hash([]byte("keep\n")), Mode: "0644"}
	if err := lock.Save(filepath.Join(m.Root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.Root, "keep.txt"), []byte("keep\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(t.Context(), "example"); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, comment := range []string{"# keep this", "# inline", "# keep plugin comment", "# environment comment", "host = 'localhost'", "name = 'second'"} {
		if !bytes.Contains(data, []byte(comment)) {
			t.Errorf("lost %q: %s", comment, data)
		}
	}
	if _, err := os.Stat(filepath.Join(m.Root, "keep.txt")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Out.(*bytes.Buffer).String(), "keep.txt") {
		t.Fatal("owned file missing from output")
	}
}

func TestTrustAndDryRun(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                    string
		trust, interactive, dry bool
		answer                  string
		want                    bool
	}{
		{name: "missing-trust"}, {name: "declined", interactive: true, answer: "no\n"},
		{name: "confirmed", interactive: true, answer: "yes\n", want: true},
		{name: "flag", trust: true, want: true}, {name: "preview", dry: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := manager(t)
			m.Trust = tc.trust
			m.Interactive = tc.interactive
			m.DryRun = tc.dry
			m.In = strings.NewReader(tc.answer)
			err := m.Add(t.Context(), "path:"+fixture(t), "")
			if (err == nil) != tc.want {
				t.Fatalf("got %v", err)
			}
			entries, err := m.List()
			if err != nil {
				t.Fatal(err)
			}
			expected := 0
			if tc.want && !tc.dry {
				expected = 1
			}
			if len(entries) != expected {
				t.Fatal(entries)
			}
			if tc.dry {
				files, err := os.ReadDir(m.CacheDir)
				if err != nil || len(files) != 0 {
					t.Fatalf("preview wrote cache: %v %v", files, err)
				}
			}
		})
	}
}

type fixtureRunner struct {
	source string
	calls  []execx.Cmd
}

func (r *fixtureRunner) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	r.calls = append(r.calls, c)
	if len(r.calls) == 1 {
		destination := c.Args[len(c.Args)-1]
		tr, err := readTree(r.source)
		if err != nil {
			return execx.Result{}, err
		}
		for _, file := range tr.plan.Files {
			path := filepath.Join(destination, file.Path)
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return execx.Result{}, err
			}
			if err := os.WriteFile(path, file.Data, file.Mode); err != nil {
				return execx.Result{}, err
			}
		}
		return execx.Result{}, nil
	}
	if len(r.calls) == 2 {
		return execx.Result{Stdout: []byte("abc HEAD\nabc refs/tags/v1.0.0\n")}, nil
	}
	return execx.Result{}, nil
}

func TestGitFetchWithFakeRunner(t *testing.T) {
	t.Parallel()
	m := manager(t)
	runner := &fixtureRunner{source: fixture(t)}
	m.Runner = runner
	if err := m.Add(t.Context(), "git:https://example.com/repo", "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 3 {
		t.Fatal(runner.calls)
	}
	// Resolution uses the approved cache. Verify fetches again.
	if _, err := m.resolve(t.Context(), plugin.Options{Root: m.Root}); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 3 {
		t.Fatal("resolution fetched despite its cache")
	}
}

func TestTagVerification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, out string
		valid     bool
	}{
		{"tag", "abc HEAD\nabc refs/tags/v1\n", true},
		{"annotated", "abc HEAD\ndef refs/tags/v1\nabc refs/tags/v1^{}\n", true},
		{"branch", "abc HEAD\nabc refs/heads/v1\n", false},
		{"other-commit", "abc HEAD\ndef refs/tags/v1\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := verifyTag([]byte(tc.out), "v1"); (err == nil) != tc.valid {
				t.Fatalf("got %v", err)
			}
		})
	}
}
