package stackversions

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// usesPattern matches a literal "uses: owner/repo[/path]@ref" line. A local action (./...) has no match.
var usesPattern = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*([A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+)@(\S+)`)

// actionKey returns the stackversions key of an action: owner/repo, without a sub-path.
func actionKey(name string) string {
	parts := strings.Split(name, "/")
	return parts[0] + "/" + parts[1]
}

// TestWorkflowActionPins checks that each pinned action in the repository workflows and in the
// template workflows uses the commit SHA in stackversions.yaml (ADR 0018, the docs.yml defect).
func TestWorkflowActionPins(t *testing.T) {
	t.Parallel()

	stack, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	root := filepath.Join("..", "..")
	var files []string
	workflows, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, workflows...)
	packs := filepath.Join(root, "internal", "templates", "packs")
	err = filepath.WalkDir(packs, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasSuffix(path, ".yml.tmpl") {
			files = append(files, path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(workflows) == 0 || len(files) == len(workflows) {
		t.Fatalf("found %d workflows and %d files in total; the search paths are wrong", len(workflows), len(files))
	}
	checked := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for index, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "[[") {
				continue
			}
			match := usesPattern.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			name, ref := match[1], match[2]
			where := file + ":" + strconv.Itoa(index+1)
			pin, ok := stack.Actions[actionKey(name)]
			if !ok {
				t.Errorf("%s: action %s has no pin in stackversions.yaml\nTry: add %s to actions", where, name, actionKey(name))
				continue
			}
			if ref != pin.SHA {
				t.Errorf("%s: action %s uses %s, want %s (%s)\nTry: use the SHA from stackversions.yaml", where, name, ref, pin.SHA, pin.Tag)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no pinned actions found; the pattern is wrong")
	}
}
