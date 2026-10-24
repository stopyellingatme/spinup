package scaffold

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestCreate(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"pack", "exec"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			dir, err := Create(Options{Dir: root, Name: "sample", Kind: kind, SDKPath: "/local/sdk"})
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"plugin.toml", "pack.toml", "files/note.txt.tmpl", "README.md"} {
				if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "exec" {
				// A rooted path is not absolute on Windows, so Create joins it to the directory.
				wantSDK := "/local/sdk"
				if !filepath.IsAbs(wantSDK) {
					wantSDK = filepath.Join(root, wantSDK)
				}
				data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
				if err != nil || !strings.Contains(string(data), "=> "+strconv.Quote(wantSDK)) {
					t.Fatalf("go.mod = %s, %v", data, err)
				}
			}
			if _, err := Create(Options{Dir: root, Name: "sample", Kind: kind}); err == nil {
				t.Fatal("overwrote a non-empty directory")
			}
		})
	}
}

func TestRejectInvalidOptions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, kind string }{
		{"../escape", "pack"}, {"Upper", "pack"}, {"", "pack"}, {"valid", "other"}, {"con", "pack"},
		{strings.Repeat("a", 58), "exec"}, {strings.Repeat("a", 58), "pack"},
	} {
		t.Run(tc.name+tc.kind, func(t *testing.T) {
			t.Parallel()
			_, err := Create(Options{Dir: t.TempDir(), Name: tc.name, Kind: tc.kind})
			if err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestEmptyDirectoryAndDryRun(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "output")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	options := Options{Dir: root, Out: "output", Name: "demo", Kind: "pack", DryRun: true}
	if _, err := Create(options); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("dry run wrote files: %v, %v", entries, err)
	}
	options.DryRun = false
	if _, err := Create(options); err != nil {
		t.Fatal(err)
	}
}

func TestSymlinkDestination(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "demo")); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(Options{Dir: root, Name: "demo", Kind: "pack"}); err == nil {
		t.Fatal("accepted a symlink destination")
	}
}

func TestDeterministicOutput(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"pack", "exec"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			options := Options{Name: "demo", Kind: kind, Platform: "linux-amd64"}
			first, err := render(options)
			if err != nil {
				t.Fatal(err)
			}
			second, err := render(options)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(first, second) {
				t.Fatal("identical options produced different files")
			}
		})
	}
}
