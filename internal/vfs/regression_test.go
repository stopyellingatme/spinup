package vfs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCleanPathPortable(t *testing.T) {
	t.Parallel()
	invalid := []string{"a:b", "x:y", "a\\b", "a\x00b", "a\nb", "a\tb", "a\x7fb", "a\u0085b", "file.", "file ", "dir./file", "dir /file", "dir/./file"}
	devices := []string{"CON", "PRN", "AUX", "NUL"}
	for i := 1; i <= 9; i++ {
		devices = append(devices, fmt.Sprintf("COM%d", i), fmt.Sprintf("LPT%d", i))
	}
	for _, device := range devices {
		invalid = append(invalid, device, strings.ToLower(device)+".txt", "dir/"+device+".tar.gz", device+"/file")
	}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := CleanPath(name); err == nil || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("CleanPath(%q) error = %v", name, err)
			}
		})
	}
	for _, name := range []string{"COM0", "COM10", "LPT0.txt", "LPT10", "console.txt", "auxiliary", "dir//file"} {
		if _, err := CleanPath(name); err != nil {
			t.Errorf("CleanPath(%q) error = %v", name, err)
		}
	}
}

func TestPlanCaseCollisions(t *testing.T) {
	t.Parallel()
	for _, pair := range [][2]string{{"README.md", "readme.md"}, {"dir/K.txt", "dir/K.txt"}} {
		t.Run(pair[0], func(t *testing.T) {
			t.Parallel()
			plan := mustPlan(t, File{Path: pair[0], Data: []byte("one\n")})
			err := plan.Add(File{Path: pair[1], Data: []byte("two\n")})
			assertPathError(t, err, pair[:]...)
			if len(plan.Files) != 1 || string(plan.Files[0].Data) != "one\n" {
				t.Fatalf("Add changed the plan: %#v", plan)
			}
			_, err = plan.Merge(Plan{Files: []File{{Path: pair[1]}}})
			assertPathError(t, err, pair[:]...)
			_, err = (Plan{Files: []File{{Path: pair[0]}, {Path: pair[1]}}}).Merge(Plan{})
			assertPathError(t, err, pair[:]...)
			root := t.TempDir()
			invalid := Plan{Files: []File{{Path: pair[0]}, {Path: pair[1]}}}
			_, err = Diff(root, invalid)
			assertPathError(t, err, pair[:]...)
			_, err = Apply(root, invalid, ApplyOptions{})
			assertPathError(t, err, pair[:]...)
		})
	}
}

func TestDiskCaseCollisions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ existing, planned, collision string }{
		{"README.md", "readme.md", "README.md"}, {"dir/README.md", "dir/readme.md", "dir/README.md"}, {"Dir/file", "dir/new", "Dir"},
	} {
		t.Run(test.planned, func(t *testing.T) {
			t.Parallel()
			for _, remove := range []bool{false, true} {
				root := t.TempDir()
				writeFile(t, root, test.existing, "original\n")
				plan := mustPlan(t, File{Path: test.planned, Data: []byte("replacement\n"), Delete: remove})
				_, err := Diff(root, plan)
				assertPathError(t, err, test.collision, test.planned)
				_, err = Apply(root, plan, ApplyOptions{})
				assertPathError(t, err, test.collision, test.planned)
				if got := readFile(t, root, test.existing); got != "original\n" {
					t.Fatalf("existing file changed to %q", got)
				}
			}
		})
	}
}

func TestFinalSymlinks(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"inside", "missing", "outside"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			for _, remove := range []bool{false, true} {
				root := t.TempDir()
				writeFile(t, root, "inside", "original\n")
				destination := filepath.Join(root, target)
				if target == "outside" {
					destination = filepath.Join(t.TempDir(), "file")
					if err := os.WriteFile(destination, []byte("outside\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				link := filepath.Join(root, "link")
				if err := os.Symlink(destination, link); err != nil {
					t.Fatal(err)
				}
				plan := mustPlan(t, File{Path: "link", Data: []byte("replacement\n"), Delete: remove})
				_, err := Diff(root, plan)
				assertSymlinkError(t, err)
				_, err = Apply(root, plan, ApplyOptions{})
				assertSymlinkError(t, err)
				info, err := os.Lstat(link)
				if err != nil || info.Mode()&fs.ModeSymlink == 0 {
					t.Fatalf("link replaced: %v, %v", info, err)
				}
			}
		})
	}
}

func TestApplyWithoutTextDiff(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "update", "old\n")
	writeFile(t, root, "remove", "removed\n")
	plan := mustPlan(t, File{Path: "update", Data: []byte("new\n")}, File{Path: "create", Data: []byte("created\n")}, File{Path: "remove", Delete: true})
	dryRun, err := Apply(root, plan, ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range dryRun {
		if change.UnifiedDiff == "" {
			t.Errorf("dry-run diff for %q is empty", change.Path)
		}
	}
	changes, err := Apply(root, plan, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range changes {
		if change.UnifiedDiff != "" {
			t.Errorf("Apply computed a diff for %q", change.Path)
		}
	}
}

func TestApplyRejectsChangedPath(t *testing.T) {
	// The hook is global, so this test must remain serial.
	for _, test := range []struct{ name, entry string }{{"symlink", "b.txt"}, {"case collision", "B.txt"}} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "a.txt", "original\n")
			oldHook := applyHook
			t.Cleanup(func() { applyHook = oldHook })
			applyHook = func(name string) error {
				if name != "b.txt" {
					return nil
				}
				if test.name == "symlink" {
					return os.Symlink(filepath.Join(root, "a.txt"), filepath.Join(root, test.entry))
				}
				return os.WriteFile(filepath.Join(root, test.entry), []byte("original\n"), 0o644)
			}
			_, err := Apply(root, mustPlan(t, File{Path: "a.txt", Data: []byte("updated\n")}, File{Path: "b.txt", Data: []byte("created\n")}), ApplyOptions{})
			if test.name == "symlink" {
				assertSymlinkPathError(t, err, "b.txt")
			} else {
				assertPathError(t, err, "b.txt", "B.txt")
			}
			if got := readFile(t, root, "a.txt"); got != "original\n" {
				t.Fatalf("rollback content = %q", got)
			}
			info, statErr := os.Lstat(filepath.Join(root, test.entry))
			if statErr != nil {
				t.Fatal(statErr)
			}
			if test.name == "symlink" && info.Mode()&fs.ModeSymlink == 0 {
				t.Fatal("Apply replaced the symlink")
			}
			if got := readFile(t, root, test.entry); got != "original\n" {
				t.Fatalf("conflicting entry changed to %q", got)
			}
		})
	}
}

func TestDiffFinalNewline(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, old, new, want string }{
		{"add newline", "a", "a\n", "-a\n\\ No newline at end of file\n+a\n"},
		{"remove newline", "a\n", "a", "-a\n+a\n\\ No newline at end of file\n"},
		{"both missing", "a", "b", "-a\n\\ No newline at end of file\n+b\n\\ No newline at end of file\n"},
		{"context missing", "a\nz", "b\nz", "-a\n+b\n z\n\\ No newline at end of file\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeFile(t, root, "file", test.old)
			changes, err := Diff(root, mustPlan(t, File{Path: "file", Data: []byte(test.new)}))
			if err != nil {
				t.Fatal(err)
			}
			if changes[0].Kind != Update || !strings.HasSuffix(changes[0].UnifiedDiff, test.want) {
				t.Fatalf("change = %#v; want diff suffix %q", changes[0], test.want)
			}
		})
	}
}

func TestDiffSizeLimit(t *testing.T) {
	t.Parallel()
	large := []byte(strings.Repeat("a", (1<<20)+1))
	for _, test := range []struct {
		name     string
		old, new []byte
	}{
		{"old too large", large, []byte("new\n")}, {"new too large", []byte("old\n"), large}, {"create too large", nil, large}, {"remove too large", large, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if test.old != nil {
				if err := os.WriteFile(filepath.Join(root, "file"), test.old, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			changes, err := Diff(root, mustPlan(t, File{Path: "file", Data: test.new, Delete: test.new == nil}))
			if err != nil {
				t.Fatal(err)
			}
			got := changes[0].UnifiedDiff
			want := fmt.Sprintf("File too large to diff (%d bytes)", len(large))
			if got != want {
				t.Fatalf("diff length = %d; want %q", len(got), want)
			}
		})
	}
}

func TestPatienceDiff(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, old, new, kinds string }{
		{"empty", "", "", ""},
		{"create", "", "a\nb\n", "++"},
		{"remove", "a\nb\n", "", "--"},
		{"equal", "a\na\n", "a\na\n", "  "},
		{"prefix and suffix", "a\nx\nz\n", "a\ny\nz\n", " -+ "},
		{"unique anchor", "x\na\ny\n", "u\na\nv\n", "-+ -+"},
		{"crossed anchors", "a\nb\nc\n", "b\na\nc\n", "- + "},
		{"repeated region", "x\na\na\ny\n", "u\na\na\nv\n", "----++++"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lines := patienceDiff(splitLines([]byte(test.old)), splitLines([]byte(test.new)))
			var old, new, kinds strings.Builder
			for _, line := range lines {
				kinds.WriteByte(line.kind)
				if line.kind != '+' {
					old.WriteString(line.text)
				}
				if line.kind != '-' {
					new.WriteString(line.text)
				}
			}
			if old.String() != test.old || new.String() != test.new || kinds.String() != test.kinds {
				t.Fatalf("reconstructed old=%q, new=%q, kinds=%q; want %q, %q, %q", old.String(), new.String(), kinds.String(), test.old, test.new, test.kinds)
			}
		})
	}
}

func TestDiffLargeInputs(t *testing.T) {
	// Keep this test serial because MemStats measures the whole process.
	var old, new strings.Builder
	for i := range 20000 {
		fmt.Fprintf(&old, "old %d\n", i)
		fmt.Fprintf(&new, "new %d\n", i)
	}
	beforeData, afterData := []byte(old.String()), []byte(new.String())
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	got := fileDiff("file", beforeData, afterData)
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("20,000-line diff: %s, %d bytes allocated", elapsed, allocated)
	if elapsed >= 2*time.Second || allocated >= 50<<20 {
		t.Fatalf("diff exceeded budget: %s, %d bytes", elapsed, allocated)
	}
	if !strings.Contains(got, "@@ -1,20000 +1,20000 @@") || !strings.Contains(got, "-old 19999\n") || !strings.Contains(got, "+new 19999\n") {
		t.Fatal("diff omitted lines")
	}
}

func assertPathError(t *testing.T, err error, paths ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a path error")
	}
	for _, name := range paths {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %q", err, name)
		}
	}
}

func assertSymlinkError(t *testing.T, err error) {
	t.Helper()
	assertSymlinkPathError(t, err, "link")
}

func assertSymlinkPathError(t *testing.T, err error, name string) {
	t.Helper()
	assertPathError(t, err, name)
	if !strings.HasSuffix(err.Error(), "Try: replace the symlink with a regular file, or remove it from the template") {
		t.Fatalf("symlink error = %v", err)
	}
}
