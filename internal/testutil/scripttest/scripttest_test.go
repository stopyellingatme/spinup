package scripttest

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean/dofake"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestScripts(t *testing.T) {
	root := testutil.RepoRoot(t)
	rust := os.Getenv("SPINUP_BIN") != ""
	implementation := "go"
	if rust {
		implementation = "rust"
	}
	pattern := os.Getenv("SPINUP_SCRIPT_RUN")
	files, err := selectScripts(root, pattern, rust)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Skipf("no scripts match SPINUP_SCRIPT_RUN=%q (app gates require SPINUP_APP_GATE=1)", pattern)
	}
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "spinup")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if source := os.Getenv("SPINUP_BIN"); source != "" {
		if err := copyBinary(root, source, binary); err != nil {
			t.Fatal(err)
		}
	} else {
		// Building this test binary is the permitted os/exec exception.
		cmd := exec.CommandContext(t.Context(), "go", "build", "-o", binary, filepath.Join(root, "cmd", "spinup"))
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build spinup: %v\n%s\nTry: correct the build failure", err, output)
		}
	}
	if rust && os.Getenv("SPINUP_KIT_DIR") != "" {
		files = prepareRustScripts(t, files)
	}
	limit, err := scriptParallel(os.Getenv("SPINUP_SCRIPT_PARALLEL"))
	if err != nil {
		t.Fatal(err)
	}
	var slots chan struct{}
	if limit > 0 {
		slots = make(chan struct{}, limit)
	}
	// Scripts get their own HOME, so pin the Go caches to the real ones. Otherwise an
	// app-gate build would start with an empty module cache and fail offline.
	goEnv := exec.CommandContext(t.Context(), "go", "env", "GOCACHE", "GOMODCACHE")
	goEnv.Dir = root
	out, err := goEnv.Output()
	if err != nil {
		t.Fatalf("go env: %v\nTry: check the Go installation", err)
	}
	caches := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(caches) != 2 {
		t.Fatalf("go env printed %q, want two lines", out)
	}
	deadline, _ := t.Deadline()
	testscript.RunT(scriptT{T: t, slots: slots}, testscript.Params{
		Files:    files,
		Deadline: deadline,
		Condition: func(cond string) (bool, error) {
			return scriptCondition(implementation, cond)
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			// dofake start runs the DigitalOcean API fake and points the CLI at it.
			"dofake": func(ts *testscript.TestScript, neg bool, args []string) {
				if neg || len(args) != 1 || args[0] != "start" {
					ts.Fatalf("usage: dofake start")
				}
				srv := dofake.New(dofake.Options{ClientID: "spinup-test-client", StaticTokens: []string{"static-test-token"}})
				url := srv.Start()
				ts.Defer(srv.Close)
				ts.Setenv("SPINUP_DO_API_BASE", url)
				ts.Setenv("SPINUP_DO_OAUTH_BASE", url)
				ts.Setenv("SPINUP_DO_CLIENT_ID", "spinup-test-client")
				ts.Setenv("SPINUP_CREDENTIALS_DIR", ts.MkAbs("creds"))
				ts.Setenv("SPINUP_BROWSER", "curl -fsSL")
			},
			"mkversion": func(ts *testscript.TestScript, neg bool, args []string) {
				if neg || len(args) < 1 || len(args) > 2 {
					ts.Fatalf("usage: mkversion <version> [overlay-dir]")
				}
				if err := makeVersion(root, ts.MkAbs("templates"), args[0], overlayArg(ts, args), ts.Getenv); err != nil {
					ts.Fatalf("mkversion: %v", err)
				}
			},
		},
		Setup: func(env *testscript.Env) error {
			// Scripts have their own HOME, so give git an identity for commits.
			for key, value := range map[string]string{
				"GIT_AUTHOR_NAME": "spinup test", "GIT_AUTHOR_EMAIL": "test@example.com",
				"GIT_COMMITTER_NAME": "spinup test", "GIT_COMMITTER_EMAIL": "test@example.com",
			} {
				env.Setenv(key, value)
			}
			env.Setenv("GOCACHE", caches[0])
			env.Setenv("GOMODCACHE", caches[1])
			// pnpm and corepack also find their caches through HOME; pin them so web
			// scripts install offline from the real store.
			for key, value := range nodeCaches() {
				env.Setenv(key, value)
			}
			for key, value := range rustCaches() {
				env.Setenv(key, value)
			}
			for _, key := range []string{"SPINUP_PACKS_SRC", "SPINUP_STACK_SRC", "SPINUP_KIT_DIR", "RUSTUP_TOOLCHAIN", "RUSTUP_AUTO_INSTALL", "CARGO_NET_OFFLINE", "CARGO_TARGET_DIR", "CARGO_BUILD_JOBS", "SQLX_OFFLINE", "GOTOOLCHAIN", "GOFLAGS", "GOPROXY", "PATH", "DATABASE_URL", "CI"} {
				if value, ok := os.LookupEnv(key); ok {
					if value != "" && (key == "SPINUP_PACKS_SRC" || key == "SPINUP_STACK_SRC" || key == "SPINUP_KIT_DIR") {
						value = sourcePath(root, value, "")
					}
					env.Setenv(key, value)
				}
			}
			// Scripts that migrate must not share a database: give each one its own.
			if base := os.Getenv("DATABASE_URL"); base != "" {
				url, err := scriptDatabase(env, base)
				if err != nil {
					return err
				}
				env.Setenv("DATABASE_URL", url)
			}
			// SPINUP_REPO lets app-gate scripts point SPINUP_KIT_REPLACE at $SPINUP_REPO/kit.
			env.Setenv("SPINUP_REPO", root)
			env.Setenv("PATH", binDir+string(os.PathListSeparator)+env.Getenv("PATH"))
			env.Setenv("HOME", env.WorkDir)
			env.Setenv("SPINUP_NO_UPDATE_CHECK", "1")
			env.Setenv("NO_COLOR", "1")
			return nil
		},
	})
}

func overlayArg(ts *testscript.TestScript, args []string) string {
	if len(args) < 2 {
		return ""
	}
	return ts.MkAbs(args[1])
}

// makeVersion writes a synthetic template version for SPINUP_TEMPLATES_DIR (ADR 0006 §3):
// <dir>/<version>/packs and stackversions.yaml copied from the repository, then the files of
// overlay (which mirrors that layout) copied on top. Each line of <overlay>/DELETE names a
// path, relative to the version directory, to remove.
func makeVersion(root, dir, version, overlay string, getenv func(string) string) error {
	packs := sourcePath(root, getenv("SPINUP_PACKS_SRC"), "internal/templates/packs")
	stackSource := sourcePath(root, getenv("SPINUP_STACK_SRC"), "internal/stackversions/stackversions.yaml")
	target := filepath.Join(dir, version)
	if err := os.CopyFS(filepath.Join(target, "packs"), os.DirFS(packs)); err != nil {
		return fmt.Errorf("copy template packs from %q: %w\nTry: set SPINUP_PACKS_SRC to a readable pack directory", packs, err)
	}
	stack, err := os.ReadFile(stackSource)
	if err != nil {
		return fmt.Errorf("read stack versions from %q: %w\nTry: set SPINUP_STACK_SRC to a readable YAML file", stackSource, err)
	}
	if err := os.WriteFile(filepath.Join(target, "stackversions.yaml"), stack, 0o644); err != nil {
		return err
	}
	if overlay == "" {
		return nil
	}
	return filepath.WalkDir(overlay, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(overlay, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if rel == "DELETE" {
			for _, line := range strings.Split(string(data), "\n") {
				if line = strings.TrimSpace(line); line != "" {
					if err := os.RemoveAll(filepath.Join(target, filepath.FromSlash(line))); err != nil {
						return err
					}
				}
			}
			return nil
		}
		dest := filepath.Join(target, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o644)
	})
}

// nodeCaches returns the variables that let pnpm (scripts/bin/pnpm) and corepack use the real
// user's home, store, and caches while the script has its own HOME.
func nodeCaches() map[string]string {
	caches := map[string]string{}
	home, err := os.UserHomeDir()
	if err != nil {
		return caches
	}
	caches["SPINUP_PNPM_HOME"] = home
	// Scripts install offline from this pnpm store. CI sets SPINUP_PNPM_STORE (or
	// npm_config_store_dir) to the store that scripts/ci/warm-cache.sh filled.
	// Without them, use the default store of pnpm on this platform.
	for _, key := range []string{"npm_config_store_dir", "npm_config_cache_dir"} {
		if value := os.Getenv(key); value != "" {
			caches[key] = value
		}
	}
	switch {
	case os.Getenv("SPINUP_PNPM_STORE") != "":
		caches["SPINUP_PNPM_STORE"] = os.Getenv("SPINUP_PNPM_STORE")
	case os.Getenv("npm_config_store_dir") != "":
		caches["SPINUP_PNPM_STORE"] = os.Getenv("npm_config_store_dir")
	case runtime.GOOS == "darwin":
		caches["SPINUP_PNPM_STORE"] = filepath.Join(home, "Library", "pnpm", "store")
	default:
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		caches["SPINUP_PNPM_STORE"] = filepath.Join(data, "pnpm", "store")
	}
	// The docker CLI finds its context (Docker Desktop's socket) through ~/.docker.
	if value := os.Getenv("DOCKER_CONFIG"); value != "" {
		caches["DOCKER_CONFIG"] = value
	} else {
		caches["DOCKER_CONFIG"] = filepath.Join(home, ".docker")
	}
	if value := os.Getenv("COREPACK_HOME"); value != "" {
		caches["COREPACK_HOME"] = value
	} else {
		caches["COREPACK_HOME"] = filepath.Join(home, ".cache", "node", "corepack")
	}
	return caches
}

var scriptDatabases atomic.Int64

// scriptDatabase creates a new, empty database next to the one in base (with psql) and returns
// its URL. The database is dropped when the script ends.
func scriptDatabase(env *testscript.Env, base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse DATABASE_URL: %w\nTry: export a postgres:// URL", err)
	}
	name := fmt.Sprintf("spinup_script_%d_%d", os.Getpid(), scriptDatabases.Add(1))
	psql := func(sql string) error {
		out, err := exec.CommandContext(context.Background(), "psql", base, "-v", "ON_ERROR_STOP=1", "-qc", sql).CombinedOutput()
		if err != nil {
			return fmt.Errorf("psql %q: %w\n%s\nTry: install the psql client, or unset DATABASE_URL", sql, err, out)
		}
		return nil
	}
	if err := psql("CREATE DATABASE " + name); err != nil {
		return "", err
	}
	env.Defer(func() { _ = psql("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)") })
	u.Path = "/" + name
	return u.String(), nil
}
