package scripttest

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rogpeppe/go-internal/testscript"
)

func TestHarnessConditions(t *testing.T) {
	for _, impl := range []string{"go", "rust"} {
		for _, want := range []string{"go", "rust"} {
			got, err := scriptCondition(impl, "impl:"+want)
			if err != nil || got != (impl == want) {
				t.Fatalf("condition %s/%s = %v, %v", impl, want, got, err)
			}
		}
	}
	if _, err := scriptCondition("go", "impl:other"); err == nil {
		t.Fatal("accepted unknown implementation")
	}
}

func TestHarnessForeignBinary(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "foreign")
	target := filepath.Join(t.TempDir(), "spinup")
	if err := os.WriteFile(source, []byte("foreign binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyBinary(root, "foreign", target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "foreign binary" {
		t.Fatalf("copied binary = %q, %v", data, err)
	}
	info, err := os.Stat(target)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("copied mode = %v, %v", info, err)
	}
	if err := copyBinary(root, "missing", target); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("missing binary: %v", err)
	}
}

func TestHarnessParity(t *testing.T) {
	t.Setenv("SPINUP_APP_GATE", "")
	root := t.TempDir()
	dir := filepath.Join(root, "testdata", "script")
	if err := os.MkdirAll(filepath.Join(dir, "rust"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"version.txtar", "later.txtar", "rust/version.txtar", "app_gate_api.txtar"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "parity.txt"), []byte("# R1 scripts\nversion # shared or Rust twin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := selectScripts(root, "*", true)
	if err != nil || len(files) != 1 || files[0] != filepath.Join(dir, "rust", "version.txtar") {
		t.Fatalf("Rust selection = %v, %v", files, err)
	}
	files, err = selectScripts(root, "*", false)
	if err != nil || len(files) != 2 {
		t.Fatalf("Go selection = %v, %v", files, err)
	}
	if _, err := selectScripts(root, "[", true); err == nil {
		t.Fatal("accepted invalid selector")
	}
	if err := os.WriteFile(filepath.Join(dir, "parity.txt"), []byte("[\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := selectScripts(root, "*", true); err == nil {
		t.Fatal("accepted invalid parity glob")
	}
}

func TestHarnessKitRewrite(t *testing.T) {
	input := []byte("env SPINUP_KIT_REPLACE=$SPINUP_REPO/kit\nenv SPINUP_KIT_REPLACE=\n-- file --\nSPINUP_KIT_REPLACE=$SPINUP_REPO/kit\n")
	want := "env SPINUP_KIT_REPLACE=$SPINUP_KIT_DIR\nenv SPINUP_KIT_REPLACE=\n-- file --\nSPINUP_KIT_REPLACE=$SPINUP_REPO/kit\n"
	if got := string(rewriteKit(input)); got != want {
		t.Fatalf("rewrite = %q", got)
	}
}

func TestHarnessMakeVersionSources(t *testing.T) {
	root := t.TempDir()
	packs := filepath.Join(root, "foreign-packs")
	stack := filepath.Join(root, "foreign-stack.yaml")
	if err := os.MkdirAll(filepath.Join(packs, "base"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packs, "base", "pack.toml"), []byte("foreign pack\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stack, []byte("foreign stack\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPINUP_PACKS_SRC", packs)
	t.Setenv("SPINUP_STACK_SRC", stack)
	target := filepath.Join(root, "templates")
	if err := makeVersion(root, target, "1.2.3", "", os.Getenv); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"packs/base/pack.toml": "foreign pack\n", "stackversions.yaml": "foreign stack\n"} {
		data, err := os.ReadFile(filepath.Join(target, "1.2.3", name))
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q, %v", name, data, err)
		}
	}
}

func TestHarnessRustCaches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, key := range []string{"CARGO_HOME", "RUSTUP_HOME"} {
		t.Setenv(key, "")
	}
	values := rustCaches()
	for key, suffix := range map[string]string{"CARGO_HOME": ".cargo", "RUSTUP_HOME": ".rustup"} {
		if values[key] != filepath.Join(os.Getenv("HOME"), suffix) {
			t.Fatalf("%s = %q", key, values[key])
		}
	}
	t.Setenv("CARGO_HOME", "custom-cache")
	want, err := filepath.Abs("custom-cache")
	if err != nil {
		t.Fatal(err)
	}
	if got := rustCaches()["CARGO_HOME"]; got != want {
		t.Fatalf("CARGO_HOME = %q, want %q", got, want)
	}
}

func TestHarnessPassEnvKeys(t *testing.T) {
	seen := map[string]bool{}
	for _, key := range passEnvKeys {
		if seen[key] {
			t.Fatalf("passEnvKeys lists %s twice", key)
		}
		seen[key] = true
	}
	for _, key := range []string{"CARGO_PROFILE_RELEASE_LTO", "CARGO_PROFILE_RELEASE_CODEGEN_UNITS", "CARGO_TARGET_DIR", "SPINUP_KIT_DIR", "DATABASE_URL"} {
		if !seen[key] {
			t.Fatalf("passEnvKeys does not pass %s", key)
		}
	}
}

func TestHarnessParallel(t *testing.T) {
	for _, value := range []string{"0", "-1", "bad"} {
		if _, err := scriptParallel(value); err == nil {
			t.Fatalf("accepted parallelism %q", value)
		}
	}
	for value, want := range map[string]int{"": 0, "1": 1, "4": 4} {
		got, err := scriptParallel(value)
		if err != nil || got != want {
			t.Fatalf("parallelism %q = %d, %v", value, got, err)
		}
	}
}

func TestHarnessParallelLimit(t *testing.T) {
	dir := t.TempDir()
	var files []string
	for i := range 6 {
		file := filepath.Join(dir, fmt.Sprintf("script-%d.txtar", i))
		if err := os.WriteFile(file, []byte("hold\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	var active, maximum atomic.Int64
	t.Run("scripts", func(t *testing.T) {
		testscript.RunT(scriptT{T: t, slots: make(chan struct{}, 2)}, testscript.Params{
			Files: files,
			Cmds: map[string]func(*testscript.TestScript, bool, []string){
				"hold": func(_ *testscript.TestScript, _ bool, _ []string) {
					n := active.Add(1)
					for previous := maximum.Load(); n > previous; previous = maximum.Load() {
						if maximum.CompareAndSwap(previous, n) {
							break
						}
					}
					time.Sleep(10 * time.Millisecond)
					active.Add(-1)
				},
			},
		})
	})
	if got := maximum.Load(); got < 1 || got > 2 {
		t.Fatalf("concurrent scripts = %d, want at most 2", got)
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("scripts still active: %d", got)
	}
}
