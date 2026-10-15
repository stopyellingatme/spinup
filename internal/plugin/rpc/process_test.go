package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stopyellingatme/spinup/internal/doctor"
	execx "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	genrun "github.com/stopyellingatme/spinup/internal/gen/run"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/plugin/pack"
	pluginrender "github.com/stopyellingatme/spinup/internal/plugin/render"
	"github.com/stopyellingatme/spinup/plugin-sdk/sdk"
)

func buildFake(t *testing.T) plugin.Loaded {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "hello")
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (execx.OSRunner{}).Run(t.Context(), execx.Cmd{Name: "go", Args: []string{"build", "-o", binary, "./internal/plugin/rpc/testdata"}, Dir: root}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	p := fakeClient(t).Plugin
	p.FS.(fstest.MapFS)["bin/hello"] = &fstest.MapFile{Data: data, Mode: 0755}
	return p
}
func TestPluginProcesses(t *testing.T) {
	t.Parallel()
	p := buildFake(t)
	for _, behavior := range []string{"success", "timeout", "huge", "bad-id", "bad-json", "api", "escape", "exit", "cancel"} {
		t.Run(behavior, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "behavior"), []byte(func() string {
				if behavior == "cancel" {
					return "timeout"
				}
				return behavior
			}()), 0644); err != nil {
				t.Fatal(err)
			}
			c := Client{Plugin: p, Root: root, Timeout: 10 * time.Second}
			ctx := t.Context()
			if behavior == "timeout" {
				c.Timeout = 100 * time.Millisecond
			}
			if behavior == "cancel" {
				cancelCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
				ctx = cancelCtx
			}
			result, err := c.Plan(ctx, sdk.PlanParams{Generator: "hello", Args: []string{"world"}})
			if err == nil {
				_, err = generatorOutput(p.Meta.Name, "hello", result)
			}
			if behavior == "success" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("%s: %v", behavior, err)
			}
			want := map[string]string{"timeout": "deadline", "cancel": "deadline", "huge": "16 MiB", "bad-id": "id", "bad-json": "invalid character", "api": "mismatch", "escape": "unsafe", "exit": "exit code"}[behavior]
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s: expected %q, got %v", behavior, want, err)
			}
		})
	}
}
func TestScrubbedEnvironment(t *testing.T) {
	// Setenv intentionally prevents this test from running in parallel.
	t.Setenv("SPINUP_TEST_SECRET", "must-not-reach-plugin")
	p := buildFake(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "behavior"), []byte("environment"), 0644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	c := Client{Plugin: p, Root: root, Stderr: &stderr}
	result, err := c.Plan(t.Context(), sdk.PlanParams{Generator: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	var observed struct {
		Env []string
		CWD string
	}
	if err := json.Unmarshal(result.Files[0].Data, &observed); err != nil {
		t.Fatal(err)
	}
	gotRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if observed.CWD != gotRoot {
		t.Fatalf("cwd=%s want=%s", observed.CWD, gotRoot)
	}
	for _, entry := range observed.Env {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "PATH", "HOME", "LANG", "TMPDIR", "SPINUP_PLUGIN_API":
		default:
			t.Errorf("inherited %s", key)
		}
	}
	if !strings.Contains(strings.Join(observed.Env, "\n"), "SPINUP_PLUGIN_API=1") {
		t.Fatal("API env missing")
	}
	if stderr.String() != "[plugin hello] first\n[plugin hello] second\n[plugin hello] partial" {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestProcessAfterHandshakeExit(t *testing.T) {
	t.Parallel()
	p := buildFake(t)
	for _, behavior := range []string{"handshake-exit", "handshake-exit-nonzero"} {
		t.Run(behavior, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "behavior"), []byte(behavior), 0644); err != nil {
				t.Fatal(err)
			}
			c := Client{Plugin: p, Root: root, Timeout: 10 * time.Second}
			for attempt := 0; attempt < 20; attempt++ {
				start := time.Now()
				_, err := c.Plan(t.Context(), sdk.PlanParams{Generator: "hello"})
				if err == nil || !strings.Contains(err.Error(), "exited") {
					t.Fatalf("attempt %d: want process exit error, got %v", attempt, err)
				}
				if behavior == "handshake-exit-nonzero" && !strings.Contains(err.Error(), "exit code 7") {
					t.Fatalf("attempt %d: lost exit status: %v", attempt, err)
				}
				if elapsed := time.Since(start); elapsed >= c.Timeout/2 {
					t.Fatalf("attempt %d: exit took %s, timeout %s", attempt, elapsed, c.Timeout)
				}
			}
		})
	}
}
func TestGeneratorRunner(t *testing.T) {
	t.Parallel()
	p := buildFake(t)
	for _, mode := range []string{"dry-run", "diff", "write", "force", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte("[app]\nname='test'\nmodule='example.com/test'\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := manifest.New().Save(filepath.Join(root, "spinup.lock")); err != nil {
				t.Fatal(err)
			}
			if mode == "force" {
				if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("old\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "symlink" {
				if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(root, "hello.txt")); err != nil {
					t.Fatal(err)
				}
			}
			all, err := Generators(t.Context(), []plugin.Loaded{p}, nil, Options{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			_, err = pluginrender.RunGenerator(t.Context(), all[0], []string{"world"}, nil, genrun.Options{Root: root, DryRun: mode == "dry-run", Diff: mode == "diff", Force: mode == "force", Out: &out})
			if mode == "symlink" {
				if err == nil {
					t.Fatal("accepted output symlink")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, readErr := os.ReadFile(filepath.Join(root, "hello.txt"))
			lock, err := manifest.Load(filepath.Join(root, "spinup.lock"))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "dry-run" || mode == "diff" {
				if !os.IsNotExist(readErr) || len(lock.Files) != 0 {
					t.Fatal("preview changed files or lock")
				}
				return
			}
			if readErr != nil || string(data) != "hello\n" || lock.Files["hello.txt"].Template != "plugin/hello/@exec/hello" {
				t.Fatalf("data=%q lock=%+v err=%v", data, lock, readErr)
			}
		})
	}
}
func TestPlanValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path, class string
		mode              uint32
	}{
		{"escape", "../outside", "managed", 0644}, {"absolute", "/tmp/outside", "managed", 0644}, {"protected", ".spinup/plugins.allow", "managed", 0644}, {"class", "safe.txt", "unknown", 0644}, {"mode", "safe.txt", "managed", 04755},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := generatorOutput("hello", "hello", sdk.PlanResult{Files: []sdk.File{{Path: tc.path, Class: tc.class, Mode: tc.mode}}})
			if err == nil {
				t.Fatal("accepted invalid plan")
			}
		})
	}
}
func TestCollision(t *testing.T) {
	t.Parallel()
	p := buildFake(t)
	_, err := Generators(t.Context(), []plugin.Loaded{p}, []gen.Generator{{Name: "builtin", Aliases: []string{"hello"}}}, Options{Root: t.TempDir()})
	if err == nil {
		t.Fatal("accepted alias collision")
	}
}
func TestDoctorChecks(t *testing.T) {
	p := buildFake(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	source := filepath.Join(root, "plugins", "hello")
	if err := os.CopyFS(source, p.FS); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "pack.toml"), []byte("name='hello'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "spinup.toml"), []byte("[app]\nname='test'\nmodule='example.com/test'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := manifest.New().Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	m := pack.Manager{Root: root, CacheDir: t.TempDir(), TrustExec: "hello", CLIVersion: "dev"}
	// The complete metadata is required by pack validation.
	metadata := "name='hello'\nversion='1'\napi=1\nspinup='*'\nkind='exec'\n[exec]\nbinaries={'" + runtime.GOOS + "-" + runtime.GOARCH + "'='bin/hello'}"
	if err := os.WriteFile(filepath.Join(source, "plugin.toml"), []byte(metadata), 0644); err != nil {
		t.Fatal(err)
	}
	if err := m.Add(t.Context(), "path:./plugins/hello", ""); err != nil {
		t.Fatal(err)
	}
	// Resolve reads local source bytes, so the default resolver needs no cache override.
	checks, err := ProjectChecks(t.Context(), Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 {
		t.Fatalf("%v", checks)
	}
	report := checks[0].Run(t.Context(), &doctor.Context{Root: root})
	if report.Status != doctor.Warn || report.Try != "repair hello" {
		t.Fatalf("%+v", report)
	}
	if err := RegisterProjectChecks(t.Context(), Options{Root: root}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range doctor.Checks(doctor.Local, false) {
		if check.ID == "plugin/hello/hello" {
			found = true
		}
	}
	if !found {
		t.Fatal("doctor registration missing")
	}
}

func TestPlanPermissions(t *testing.T) {
	t.Parallel()
	for _, mode := range []uint32{0, 0600, 0644, 0755} {
		output, err := generatorOutput("hello", "hello", sdk.PlanResult{Files: []sdk.File{{Path: "hello.txt", Class: "managed", Mode: mode}}})
		if err != nil {
			t.Fatal(err)
		}
		want := mode
		if want == 0 {
			want = 0644
		}
		if uint32(output.Plan.Files[0].Mode) != want {
			t.Fatalf("mode=%o want=%o", output.Plan.Files[0].Mode, want)
		}
	}
}
