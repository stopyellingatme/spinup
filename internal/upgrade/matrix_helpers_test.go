package upgrade_test

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/templates/store"
)

// matrixExec isolates Git identity and configuration for every upgrade subprocess.
type matrixExec struct{}

// Run executes a command with an isolated Git identity.
func (matrixExec) Run(ctx context.Context, command exec.Cmd) (exec.Result, error) {
	command.Env = append(command.Env,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=Spinup Matrix", "GIT_AUTHOR_EMAIL=matrix@example.com",
		"GIT_COMMITTER_NAME=Spinup Matrix", "GIT_COMMITTER_EMAIL=matrix@example.com",
	)
	return (exec.OSRunner{}).Run(ctx, command)
}

func matrixCommand(t *testing.T, ctx context.Context, dir, name string, args ...string) string {
	t.Helper()
	result, err := (matrixExec{}).Run(ctx, exec.Cmd{Name: name, Args: args, Dir: dir})
	if err != nil {
		t.Fatalf("%s %v: %v\n%s%s", name, args, err, result.Stdout, result.Stderr)
	}
	return string(result.Stdout)
}

func matrixRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func matrixWrite(t *testing.T, path string, data []byte, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

// matrixSource mirrors mkversion without importing the script harness.
func matrixSource(t *testing.T, repo string) templates.Source {
	t.Helper()
	extra := t.TempDir()
	target := filepath.Join(extra, "0.0.1")
	if err := os.CopyFS(filepath.Join(target, "packs"), templates.Embedded()); err != nil {
		t.Fatal(err)
	}
	matrixWrite(t, filepath.Join(target, "stackversions.yaml"), matrixRead(t, filepath.Join(repo, "internal/stackversions/stackversions.yaml")), 0o644)
	matrixOverlay(t, filepath.Join(repo, "testdata/corpus/_next"), target)
	source, err := store.New(store.Options{Current: store.Current("dev"), ExtraDir: extra, CacheDir: t.TempDir(), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

// matrixOverlay overwrites files, then applies the optional relative DELETE list.
func matrixOverlay(t *testing.T, overlay, target string) {
	t.Helper()
	err := filepath.WalkDir(overlay, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if !entry.Type().IsRegular() {
			t.Fatalf("overlay requires regular files: %s", path)
		}
		rel, err := filepath.Rel(overlay, path)
		if err != nil {
			return err
		}
		if rel == "DELETE" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		matrixWrite(t, filepath.Join(target, rel), matrixRead(t, path), info.Mode().Perm())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(overlay, "DELETE"))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range strings.Split(string(data), "\n") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if !fs.ValidPath(path) || path == "." || strings.Contains(path, "\\") {
			t.Fatalf("unsafe DELETE path %q", path)
		}
		if err := os.RemoveAll(filepath.Join(target, filepath.FromSlash(path))); err != nil {
			t.Fatal(err)
		}
	}
}

func matrixPaths(t *testing.T, label string, got, want []string) {
	t.Helper()
	got, want = slices.Clone(got), slices.Clone(want)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

func matrixConflicts(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == ".spinup" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == ".git" {
			return nil
		}
		data := matrixRead(t, path)
		if bytes.HasPrefix(data, []byte("<<<<<<< ")) || bytes.Contains(data, []byte("\n<<<<<<< ")) {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// matrixResolve retains both sides of a diff3 hunk and discards the base.
// The corpus overlaps comment lines, so retaining both sides keeps valid Go.
func matrixResolve(t *testing.T, data []byte) []byte {
	t.Helper()
	var resolved strings.Builder
	state := "outside"
	for _, line := range strings.SplitAfter(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "<<<<<<< "):
			if state != "outside" {
				t.Fatal("nested conflict marker")
			}
			state = "ours"
		case strings.HasPrefix(line, "||||||| "):
			if state != "ours" {
				t.Fatal("unexpected base marker")
			}
			state = "base"
		case strings.TrimSpace(line) == "=======":
			if state != "base" {
				t.Fatal("unexpected separator")
			}
			state = "theirs"
		case strings.HasPrefix(line, ">>>>>>> "):
			if state != "theirs" {
				t.Fatal("unexpected end marker")
			}
			state = "outside"
		default:
			if state != "base" {
				resolved.WriteString(line)
			}
		}
	}
	if state != "outside" {
		t.Fatal("incomplete diff3 conflict")
	}
	return []byte(resolved.String())
}
