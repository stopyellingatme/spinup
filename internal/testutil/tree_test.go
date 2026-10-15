package testutil

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestTreeDifferences(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		want, got tree
		fragments []string
	}{
		{"equal", tree{"a": {[]byte("same\n"), 0644}}, tree{"a": {[]byte("same\n"), 0644}}, nil},
		{"all differences", tree{"changed": {[]byte("old\n"), 0644}, "missing": {nil, 0644}, "mode": {nil, 0755}}, tree{"added": {[]byte("new\n"), 0644}, "changed": {[]byte("new\n"), 0644}, "mode": {nil, 0644}}, []string{"added: added", "changed: changed", "-old", "+new", "missing: missing", "mode: mode"}},
		{"binary", tree{"a": {[]byte{0, 1}, 0644}}, tree{"a": {[]byte{0, 2}, 0644}}, []string{"binary content differs"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reports := compareTrees(tc.want, tc.got)
			if len(tc.fragments) == 0 && len(reports) != 0 {
				t.Fatalf("unexpected differences: %v", reports)
			}
			joined := strings.Join(reports, "\n")
			for _, fragment := range tc.fragments {
				if !strings.Contains(joined, fragment) {
					t.Errorf("missing %q in %s", fragment, joined)
				}
			}
		})
	}
}

func TestRewriteGolden(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "golden")
	first := tree{"stale/file": {[]byte("stale"), 0644}}
	next := tree{"plain": {[]byte("plain"), 0644}, "bin/start": {[]byte("#!/bin/sh\n"), 0755}}
	for _, files := range []tree{first, next, next} {
		if err := writeGolden(dir, files); err != nil {
			t.Fatal(err)
		}
		got, err := readGolden(dir)
		if err != nil {
			t.Fatal(err)
		}
		if differences := compareTrees(files, got); len(differences) != 0 {
			t.Fatal(differences)
		}
	}
	modes, err := os.ReadFile(filepath.Join(dir, ".modes"))
	if err != nil {
		t.Fatal(err)
	}
	if string(modes) != "0755 bin/start\n" {
		t.Fatalf("modes: %q", modes)
	}
	if _, err := os.Stat(filepath.Join(dir, "stale")); !os.IsNotExist(err) {
		t.Fatalf("stale directory remains: %v", err)
	}
}

func TestReadTree(t *testing.T) {
	t.Parallel()
	got, err := readTree(fstest.MapFS{"nested/file": {Data: []byte("content"), Mode: 0755}})
	if err != nil {
		t.Fatal(err)
	}
	if string(got["nested/file"].data) != "content" || got["nested/file"].mode != 0755 {
		t.Fatal(got)
	}
	_, err = readTree(fstest.MapFS{"link": {Mode: fs.ModeSymlink}})
	if err == nil {
		t.Fatal("accepted a symbolic link")
	}
}

func TestDiffLimit(t *testing.T) {
	t.Parallel()
	reports := compareTrees(tree{"a": {[]byte(strings.Repeat("old\n", 100)), 0644}}, tree{"a": {[]byte(strings.Repeat("new\n", 100)), 0644}})
	if len(reports) != 1 || len(strings.Split(strings.TrimSpace(reports[0]), "\n")) > 42 {
		t.Fatalf("diff too long: %v", reports)
	}
}

func TestReadGoldenModes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, manifest string
		want           fs.FileMode
		invalid        bool
	}{
		{name: "default", want: 0644},
		{name: "executable", manifest: "0755 file with spaces\n", want: 0755},
		{name: "invalid octal", manifest: "0999 file with spaces\n", invalid: true},
		{name: "missing file", manifest: "0755 absent\n", invalid: true},
		{name: "duplicate", manifest: "0755 file with spaces\n0755 file with spaces\n", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "file with spaces"), []byte("data"), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.manifest != "" {
				if err := os.WriteFile(filepath.Join(dir, ".modes"), []byte(tc.manifest), 0644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := readGolden(dir)
			if tc.invalid {
				if err == nil {
					t.Fatal("accepted invalid modes")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got["file with spaces"].mode != tc.want {
				t.Fatalf("tree: %v", got)
			}
		})
	}
}

func TestRepoRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "kit", "nested")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"go.mod":     "module github.com/stopyellingatme/spinup\n\ngo 1.26.0\n",
		"kit/go.mod": "module github.com/stopyellingatme/spinup/kit\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(nested)
	// Resolve temporary directory aliases, such as /var and /private/var on macOS.
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := filepath.EvalSymlinks(RepoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("root = %q, want %q", got, want)
	}
}

func TestDeterministic(t *testing.T) {
	t.Parallel()
	calls := 0
	Deterministic(t, func() (fs.FS, error) {
		calls++
		return fstest.MapFS{"result": {Data: []byte("same"), Mode: 0755}}, nil
	})
	if calls != 2 {
		t.Fatalf("render called %d times", calls)
	}
}
