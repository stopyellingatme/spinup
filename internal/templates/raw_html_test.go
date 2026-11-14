package templates_test

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// rawHTMLRoots are the directories that hold the files a generated app gets: the template packs
// and the generator templates.
var rawHTMLRoots = []string{"packs", filepath.Join("..", "gen")}

var rawHTMLPattern = regexp.MustCompile(`\{\s*@html\b`)

// TestNoRawHTML fails for each {@html tag in a template pack or a generator template (ADR 0036
// section 3.3). Svelte escapes text in braces. {@html} does not, so user text in it is an XSS.
func TestNoRawHTML(t *testing.T) {
	t.Parallel()
	scanned := 0
	for _, root := range rawHTMLRoots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			// A Go test may name the tag in a fixture. It is not a template.
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			scanned++
			for _, hit := range rawHTMLLines(data) {
				t.Errorf("%s:%d: {@html renders a string without escaping it, so user text becomes markup (XSS).\n  Try: render the text with {value}, or build the markup with Svelte elements.", filepath.ToSlash(path), hit)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if scanned == 0 {
		t.Fatal("TestNoRawHTML scanned no file. The test must run in internal/templates.")
	}
}

// TestRawHTMLLines checks that the scan reports the line of each tag.
func TestRawHTMLLines(t *testing.T) {
	t.Parallel()
	data := []byte("<p>{text}</p>\n<div>{@html a}</div>\n\n{@html b}{@html c}\n{ @html d}\n{\n\t@html e}\n")
	got := fmt.Sprint(rawHTMLLines(data))
	if got != "[2 4 4 5 6]" {
		t.Fatalf("rawHTMLLines = %s, want [2 4 4 5 6]", got)
	}
}

// rawHTMLLines returns the 1-based numbers of the lines where an @html tag starts.
func rawHTMLLines(data []byte) []int {
	matches := rawHTMLPattern.FindAllIndex(data, -1)
	lines := make([]int, 0, len(matches))
	for _, match := range matches {
		lines = append(lines, bytes.Count(data[:match[0]], []byte("\n"))+1)
	}
	return lines
}
