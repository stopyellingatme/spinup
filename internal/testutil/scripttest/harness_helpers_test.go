package scripttest

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
	"github.com/rogpeppe/go-internal/txtar"
)

func copyBinary(root, source, target string) error {
	source = sourcePath(root, source, "")
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read SPINUP_BIN %q: %w\nTry: build the binary and set SPINUP_BIN to its path", source, err)
	}
	if err := os.WriteFile(target, data, 0o755); err != nil {
		return fmt.Errorf("copy SPINUP_BIN to %q: %w\nTry: use a writable temporary directory", target, err)
	}
	return nil
}

func sourcePath(root, value, fallback string) string {
	if value == "" {
		value = fallback
	}
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(root, filepath.FromSlash(value))
}

func scriptCondition(implementation, condition string) (bool, error) {
	if name, ok := strings.CutPrefix(condition, "env:"); ok && name != "" {
		return os.Getenv(name) != "", nil
	}
	if name, ok := strings.CutPrefix(condition, "impl:"); ok && (name == "go" || name == "rust") {
		return implementation == name, nil
	}
	return false, fmt.Errorf("unknown condition %q\nTry: use [impl:go], [impl:rust], [env:NAME], or a built-in condition", condition)
}

func selectScripts(root, pattern string, rust bool) ([]string, error) {
	if _, err := path.Match(pattern, ""); err != nil {
		return nil, fmt.Errorf("select scripts: %w\nTry: use a valid SPINUP_SCRIPT_RUN glob", err)
	}
	dir := filepath.Join(root, "testdata", "script")
	candidates, err := filepath.Glob(filepath.Join(dir, "*.txtar"))
	if err != nil {
		return nil, err
	}
	var parity []string
	if rust {
		data, err := os.ReadFile(filepath.Join(dir, "parity.txt"))
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("read parity.txt: %w\nTry: make the script manifest readable", err)
		}
		if err == nil {
			// A present, empty manifest selects no scripts.
			parity = []string{}
			for lineNo, line := range strings.Split(string(data), "\n") {
				entry, _, _ := strings.Cut(line, "#")
				entry = strings.TrimSpace(entry)
				if entry == "" {
					continue
				}
				if _, err := path.Match(entry, ""); err != nil || len(strings.Fields(entry)) != 1 {
					return nil, fmt.Errorf("parse parity.txt:%d: invalid script glob %q\nTry: use one script name or glob per line", lineNo+1, entry)
				}
				parity = append(parity, entry)
			}
		}
		twins, err := filepath.Glob(filepath.Join(dir, "rust", "*.txtar"))
		if err != nil {
			return nil, err
		}
		byName := make(map[string]string)
		for _, file := range append(candidates, twins...) {
			byName[filepath.Base(file)] = file
		}
		candidates = nil
		for _, file := range byName {
			candidates = append(candidates, file)
		}
		slices.Sort(candidates)
	}
	var files []string
	for _, file := range candidates {
		name := strings.TrimSuffix(filepath.Base(file), ".txtar")
		if strings.HasPrefix(name, "app_gate_") && os.Getenv("SPINUP_APP_GATE") != "1" {
			continue
		}
		if pattern != "" {
			matched, _ := path.Match(pattern, name)
			if !matched {
				continue
			}
		}
		if parity != nil {
			matched := false
			for _, entry := range parity {
				if ok, _ := path.Match(entry, name); ok {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		files = append(files, file)
	}
	return files, nil
}

func rewriteKit(data []byte) []byte {
	archive := txtar.Parse(data)
	lines := strings.Split(string(archive.Comment), "\n")
	for i, line := range lines {
		lines[i] = strings.ReplaceAll(line, "SPINUP_KIT_REPLACE=$SPINUP_REPO/kit", "SPINUP_KIT_REPLACE=$SPINUP_KIT_DIR")
	}
	archive.Comment = []byte(strings.Join(lines, "\n"))
	return txtar.Format(archive)
}

func prepareRustScripts(t *testing.T, files []string) []string {
	t.Helper()
	dir := t.TempDir()
	prepared := make([]string, 0, len(files))
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, filepath.Base(file))
		if err := os.WriteFile(target, rewriteKit(data), 0o644); err != nil {
			t.Fatal(err)
		}
		prepared = append(prepared, target)
	}
	return prepared
}

func rustCaches() map[string]string {
	values := make(map[string]string)
	home, _ := os.UserHomeDir()
	for key, fallback := range map[string]string{"CARGO_HOME": ".cargo", "RUSTUP_HOME": ".rustup"} {
		if value := os.Getenv(key); value != "" {
			absolute, err := filepath.Abs(value)
			if err == nil {
				values[key] = absolute
			}
		} else if home != "" {
			values[key] = filepath.Join(home, fallback)
		}
	}
	return values
}

// passEnvKeys are the variables that a script inherits from the test process. The two
// release-profile keys let make rs-script build generated apps with a cheap release
// profile (ADR 0021 §11).
var passEnvKeys = []string{
	"SPINUP_PACKS_SRC", "SPINUP_STACK_SRC", "SPINUP_KIT_DIR",
	"RUSTUP_TOOLCHAIN", "RUSTUP_AUTO_INSTALL", "CARGO_NET_OFFLINE", "CARGO_TARGET_DIR", "CARGO_BUILD_JOBS", "SQLX_OFFLINE",
	"CARGO_PROFILE_RELEASE_LTO", "CARGO_PROFILE_RELEASE_CODEGEN_UNITS",
	"GOTOOLCHAIN", "GOFLAGS", "GOPROXY", "PATH", "DATABASE_URL", "CI",
}

func scriptParallel(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("parse SPINUP_SCRIPT_PARALLEL %q: expected a positive integer\nTry: set SPINUP_SCRIPT_PARALLEL=1 or remove it", value)
	}
	return n, nil
}

// scriptT limits scripts without changing the global Go test parallelism.
type scriptT struct {
	*testing.T
	slots chan struct{}
}

func (t scriptT) Run(name string, f func(testscript.T)) {
	t.T.Run(name, func(child *testing.T) { f(scriptT{T: child, slots: t.slots}) })
}

func (t scriptT) Parallel() {
	t.T.Parallel()
	if t.slots != nil {
		t.slots <- struct{}{}
		t.Cleanup(func() { <-t.slots })
	}
}

func (t scriptT) Verbose() bool { return testing.Verbose() }
