package pack

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTreeHash(t *testing.T) {
	t.Parallel()
	a, b := t.TempDir(), t.TempDir()
	for i, root := range []string{a, b} {
		data := "hello\n"
		if i == 1 {
			data = "hello\r\n\r\n"
		}
		if err := os.WriteFile(filepath.Join(root, "a"), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	first, err := readTree(a)
	if err != nil {
		t.Fatal(err)
	}
	second, err := readTree(b)
	if err != nil {
		t.Fatal(err)
	}
	if first.hash() != second.hash() {
		t.Fatal("line endings changed hash")
	}
	if runtime.GOOS == "windows" {
		return // Windows has no execute bit, so a mode change cannot change the hash.
	}
	if err := os.Chmod(filepath.Join(b, "a"), 0755); err != nil {
		t.Fatal(err)
	}
	second, err = readTree(b)
	if err != nil {
		t.Fatal(err)
	}
	if first.hash() == second.hash() {
		t.Fatal("mode did not change hash")
	}
}

func TestTreeRejectsUnsafeEntries(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"symlink", "large", "count"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			switch kind {
			case "symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
					t.Fatal(err)
				}
			case "large":
				if err := os.WriteFile(filepath.Join(root, "large"), []byte(strings.Repeat("x", maxBytes+1)), 0644); err != nil {
					t.Fatal(err)
				}
			case "count":
				for i := 0; i <= maxFiles; i++ {
					f, err := os.CreateTemp(root, "file")
					if err != nil {
						t.Fatal(err)
					}
					if err = f.Close(); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := readTree(root); err == nil {
				t.Fatal("unsafe tree accepted")
			}
		})
	}
}
