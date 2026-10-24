package oracle

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/vfs"
)

type pathCase struct {
	Input string `json:"input"`
	Clean string `json:"clean"`
	Valid bool   `json:"valid"`
}

func pathCases() []pathCase {
	names := []string{"", ".", "..", "../escape", "a/../b", "/absolute", "C:/absolute", "//server/share", "a:b", "x:y", "a\\b", "a\x00b", "a\nb", "a\tb", "a\x7fb", "a\u0085b", "file.", "file ", "dir./file", "dir /file", "dir/./file", "COM0", "COM10", "LPT0.txt", "LPT10", "console.txt", "auxiliary", "dir//file", "dir/", "README.md", "dir/K.txt"}
	devices := []string{"CON", "PRN", "AUX", "NUL"}
	for i := 1; i <= 9; i++ {
		devices = append(devices, fmt.Sprintf("COM%d", i), fmt.Sprintf("LPT%d", i))
	}
	for _, device := range devices {
		names = append(names, device, strings.ToLower(device)+".txt", "dir/"+device+".tar.gz", device+"/file")
	}
	var cases []pathCase
	for _, name := range names {
		clean, err := vfs.CleanPath(name)
		cases = append(cases, pathCase{name, clean, err == nil})
	}
	return cases
}

type rootedCase struct {
	Setup   string `json:"setup"`
	Path    string `json:"path"`
	Delete  bool   `json:"delete"`
	DiffOK  bool   `json:"diff_ok"`
	ApplyOK bool   `json:"apply_ok"`
}

func rootedCases(t *testing.T) []rootedCase {
	t.Helper()
	var cases []rootedCase
	for _, setup := range []string{"regular", "final-inside", "final-outside", "final-missing", "parent-outside", "parent-inside", "parent-absolute-inside", "case-file", "case-parent", "case-kelvin"} {
		for _, remove := range []bool{false, true} {
			root, outside := t.TempDir(), t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "dir/file"), []byte("original\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outside, "file"), []byte("outside\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			name := "dir/file"
			var target, link string
			switch setup {
			case "final-inside":
				target, link = "dir/file", "link"
				name = "link"
			case "final-outside":
				target, link = filepath.Join(outside, "file"), "link"
				name = "link"
			case "final-missing":
				target, link = "missing", "link"
				name = "link"
			case "parent-outside":
				target, link = outside, "link"
				name = "link/file"
			case "parent-inside":
				target, link = "dir", "link"
				name = "link/file"
			case "parent-absolute-inside":
				target, link = filepath.Join(root, "dir"), "link"
				name = "link/file"
			case "case-file":
				name = "dir/FILE"
			case "case-parent":
				name = "Dir/new"
			case "case-kelvin":
				if err := os.WriteFile(filepath.Join(root, "dir/K.txt"), []byte("original\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				name = "dir/K.txt"
			}
			if link != "" {
				if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
					t.Fatal(err)
				}
			}
			plan := vfs.Plan{Files: []vfs.File{{Path: name, Data: []byte("replacement\n"), Delete: remove}}}
			_, diffErr := vfs.Diff(root, plan)
			_, applyErr := vfs.Apply(root, plan, vfs.ApplyOptions{})
			cases = append(cases, rootedCase{setup, name, remove, diffErr == nil, applyErr == nil})
			data, err := os.ReadFile(filepath.Join(outside, "file"))
			if err != nil || string(data) != "outside\n" {
				t.Fatalf("outside file changed: %v", err)
			}
		}
	}
	return cases
}

type planCase struct {
	Paths []string `json:"paths"`
	Valid bool     `json:"valid"`
}

func planCases() []planCase {
	var cases []planCase
	for _, names := range [][]string{{"README.md", "readme.md"}, {"dir/K.txt", "dir/K.txt"}, {"dir//file", "dir/file"}, {"same", "same"}, {"a", "b"}, {"../escape"}} {
		var plan vfs.Plan
		valid := true
		for _, name := range names {
			if err := plan.Add(vfs.File{Path: name}); err != nil {
				valid = false
				break
			}
		}
		cases = append(cases, planCase{names, valid})
	}
	return cases
}
