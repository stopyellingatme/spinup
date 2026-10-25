package oracle

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

type goldenFile struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Data string `json:"data"`
}

type modesCase struct {
	Name     string       `json:"name"`
	Files    []goldenFile `json:"files"`
	Manifest *string      `json:"manifest"`
	Error    string       `json:"error"`
}

// goldenReporter captures expected errors from the public Golden entry point.
type goldenReporter struct {
	testing.TB
	messages []string
}

type goldenFailure struct{}

func (r *goldenReporter) Fatalf(format string, args ...any) {
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
	panic(goldenFailure{})
}

func (r *goldenReporter) Errorf(format string, args ...any) {
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
}

func compareGolden(t *testing.T, files fstest.MapFS) (message string) {
	t.Helper()
	reporter := &goldenReporter{TB: t}
	defer func() {
		if failure := recover(); failure != nil {
			if _, expected := failure.(goldenFailure); !expected {
				panic(failure)
			}
		}
		message = strings.Join(reporter.messages, "\n")
	}()
	testutil.Golden(reporter, "oracle", files)
	return ""
}

func goldenModesCases(t *testing.T) []modesCase {
	t.Helper()
	cases := []modesCase{}
	// A subtest restores Chdir and the environment before other generators run.
	t.Run("golden modes", func(t *testing.T) {
		root := t.TempDir()
		t.Chdir(root)
		if err := os.WriteFile("go.mod", []byte("module github.com/stopyellingatme/spinup\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, item := range []struct {
			name  string
			files fstest.MapFS
		}{
			{"empty tree", fstest.MapFS{}},
			{"default mode", fstest.MapFS{"readme.txt": {Data: []byte("hello\n"), Mode: 0o644}}},
			{"sorted nondefault modes", fstest.MapFS{
				"z.sh":          {Data: []byte("#!/bin/sh\n"), Mode: 0o755},
				"a/config":      {Data: []byte("private\n"), Mode: 0o600},
				"a/default.txt": {Data: []byte{}, Mode: 0o644},
				"a/space name":  {Data: []byte("space\n"), Mode: 0o640},
				"é.txt":         {Data: []byte("unicode\n"), Mode: 0o664},
			}},
		} {
			t.Setenv("SPINUP_GOLDEN_UPDATE", "1")
			testutil.Golden(t, "oracle", item.files)
			data, err := os.ReadFile("testdata/golden/oracle/.modes")
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			files := describeGoldenFiles(item.files)
			cases = append(cases, modesCase{item.name, files, &text, ""})
			// The manifest must override checkout permissions, including omitted 0644 entries.
			for name := range item.files {
				if err := os.Chmod(filepath.Join("testdata/golden/oracle", name), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("SPINUP_GOLDEN_UPDATE", "")
			if message := compareGolden(t, item.files); message != "" {
				t.Fatal(message)
			}
		}
		source := fstest.MapFS{"file": {Data: []byte("data\n"), Mode: 0o644}, "space name": {Data: []byte("space\n"), Mode: 0o644}}
		t.Setenv("SPINUP_GOLDEN_UPDATE", "1")
		testutil.Golden(t, "oracle", source)
		t.Setenv("SPINUP_GOLDEN_UPDATE", "")
		manifests := []string{"", "\n\n", "0644 file\n", "644 file", "0644 space name\n", "0644 file\n\n0644 space name\n", "0755 file\n", "0000 file\n", "0644 missing\n", "0644 file\n0600 file\n", "0999 file\n", "1000 file\n", "0644\n", "0644  file\n", "0644 file\r\n", "0644 .modes\n", "0644 ../file\n"}
		for index, text := range manifests {
			if err := os.WriteFile("testdata/golden/oracle/.modes", []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			message := strings.ReplaceAll(compareGolden(t, source), root, "$ROOT")
			cases = append(cases, modesCase{fmt.Sprintf("read-%02d", index), describeGoldenFiles(source), &text, message})
		}
		if err := os.Remove("testdata/golden/oracle/.modes"); err != nil {
			t.Fatal(err)
		}
		message := strings.ReplaceAll(compareGolden(t, source), root, "$ROOT")
		cases = append(cases, modesCase{"missing manifest", describeGoldenFiles(source), nil, message})
	})
	return cases
}

func describeGoldenFiles(files fstest.MapFS) []goldenFile {
	result := make([]goldenFile, 0, len(files))
	// Sort file paths before encoding the input tree.
	for _, name := range sortedGoldenPaths(files) {
		result = append(result, goldenFile{name, fmt.Sprintf("%04o", files[name].Mode.Perm()), string(files[name].Data)})
	}
	return result
}

func sortedGoldenPaths(files fstest.MapFS) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
