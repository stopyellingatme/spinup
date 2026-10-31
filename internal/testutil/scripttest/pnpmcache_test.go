package scripttest

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// seedPnpmCache copies mutable pnpm verification state into the script's cache.
// Offline metadata stays available through a link to the real cache.
func seedPnpmCache(realHome, workDir string) (cacheDir string, err error) {
	source := os.Getenv("npm_config_cache_dir")
	if source == "" {
		if runtime.GOOS == "darwin" {
			source = filepath.Join(realHome, "Library", "Caches", "pnpm")
		} else {
			base := os.Getenv("XDG_CACHE_HOME")
			if base == "" {
				base = filepath.Join(realHome, ".cache")
			}
			source = filepath.Join(base, "pnpm")
		}
	}
	// A relative override must still point at the real metadata from the link.
	source, err = filepath.Abs(source)
	if err != nil {
		return "", fmt.Errorf("resolve pnpm cache %q: %w\nTry: set npm_config_cache_dir to an absolute cache path", source, err)
	}
	cacheDir = filepath.Join(workDir, "pnpm-cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("create pnpm cache %q: %w\nTry: check the script work directory permissions", cacheDir, err)
	}
	const filename = "lockfile-verified.jsonl"
	data, err := os.ReadFile(filepath.Join(source, filename))
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("read pnpm cache seed in %q: %w\nTry: check the real cache permissions or set npm_config_cache_dir", source, err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, filename), data, 0o644); err != nil {
		return "", fmt.Errorf("write pnpm cache seed in %q: %w\nTry: check the script cache permissions", cacheDir, err)
	}
	metadata := filepath.Join(source, "metadata")
	info, err := os.Stat(metadata)
	if os.IsNotExist(err) {
		return cacheDir, nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect pnpm metadata %q: %w\nTry: check the real cache permissions", metadata, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("inspect pnpm metadata %q: not a directory\nTry: set npm_config_cache_dir to a valid pnpm cache", metadata)
	}
	if err := os.Symlink(metadata, filepath.Join(cacheDir, "metadata")); err != nil {
		return "", fmt.Errorf("link pnpm metadata %q: %w\nTry: check symlink support and the script cache permissions", metadata, err)
	}
	return cacheDir, nil
}

func TestSeedPnpmCache(t *testing.T) {
	for _, tc := range []struct {
		name     string
		override bool
		xdg      bool
		seed     bool
	}{
		{name: "missing cache"},
		{name: "default cache", seed: true},
		{name: "cache override", override: true, xdg: true, seed: true},
		{name: "XDG cache", xdg: true, seed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			xdg := t.TempDir()
			source := filepath.Join(home, ".cache", "pnpm")
			t.Setenv("npm_config_cache_dir", "")
			t.Setenv("XDG_CACHE_HOME", "")
			if tc.xdg {
				t.Setenv("XDG_CACHE_HOME", xdg)
				source = filepath.Join(xdg, "pnpm")
			}
			if runtime.GOOS == "darwin" {
				source = filepath.Join(home, "Library", "Caches", "pnpm")
			}
			if tc.override {
				source = t.TempDir()
				t.Setenv("npm_config_cache_dir", source)
			}
			const filename = "lockfile-verified.jsonl"
			var want string
			if tc.seed {
				want = "{\"hash\":\"seed\"}\n"
				if err := os.MkdirAll(filepath.Join(source, "metadata"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(source, filename), []byte(want), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			// Each script gets a separate copy, even with the same seed.
			for range 2 {
				work := t.TempDir()
				dir, err := seedPnpmCache(home, work)
				if err != nil {
					t.Fatal(err)
				}
				if dir != filepath.Join(work, "pnpm-cache") {
					t.Fatalf("cache directory = %q, want a directory under %q", dir, work)
				}
				data, err := os.ReadFile(filepath.Join(dir, filename))
				if err != nil || string(data) != want {
					t.Fatalf("seed = %q, %v, want %q", data, err, want)
				}
				metadata := filepath.Join(dir, "metadata")
				if tc.seed {
					target, err := os.Readlink(metadata)
					if err != nil || target != filepath.Join(source, "metadata") {
						t.Fatalf("metadata link = %q, %v, want %q", target, err, filepath.Join(source, "metadata"))
					}
				} else if _, err := os.Lstat(metadata); !os.IsNotExist(err) {
					t.Fatalf("missing source metadata: got %v, want no link", err)
				}
				f, err := os.OpenFile(filepath.Join(dir, filename), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, writeErr := f.WriteString("script-only\n")
				closeErr := f.Close()
				if writeErr != nil || closeErr != nil {
					t.Fatalf("append to cache: %v, %v", writeErr, closeErr)
				}
				data, err = os.ReadFile(filepath.Join(source, filename))
				if tc.seed {
					if err != nil || string(data) != want {
						t.Fatalf("source changed: %q, %v, want %q", data, err, want)
					}
				} else if !os.IsNotExist(err) {
					t.Fatalf("missing source cache: got %v, want no file", err)
				}
			}
		})
	}
}

func TestSeedPnpmCacheErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		dir  bool
		want string
	}{
		{name: "cache directory", path: "work/pnpm-cache", want: "create pnpm cache"},
		{name: "seed read", path: "source/lockfile-verified.jsonl", dir: true, want: "read pnpm cache seed"},
		{name: "seed write", path: "work/pnpm-cache/lockfile-verified.jsonl", dir: true, want: "write pnpm cache seed"},
		{name: "metadata link", path: "work/pnpm-cache/metadata", dir: true, want: "link pnpm metadata"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			source := filepath.Join(base, "source")
			t.Setenv("npm_config_cache_dir", source)
			if err := os.MkdirAll(filepath.Join(source, "metadata"), 0o755); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(base, tc.path)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.dir {
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(target, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := seedPnpmCache(base, filepath.Join(base, "work"))
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "\nTry:") {
				t.Fatalf("error = %v, want %q and a Try line", err, tc.want)
			}
		})
	}
}
