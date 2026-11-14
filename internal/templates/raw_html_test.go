package templates_test

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rawHTMLRoots are the directories that hold the files a generated app gets: the template packs
// and the generator templates.
var rawHTMLRoots = []string{"packs", filepath.Join("..", "gen")}

// TestNoRawHTML fails for each {@html tag in a template pack or a generator template (ADR 0036
// section 3.3). Svelte escapes text in braces. {@html} does not, so user text in it is an XSS.
func TestNoRawHTML(t *testing.T) {
	t.Parallel()
	needle := []byte("{" + "@html")
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
			if !bytes.Contains(data, needle) {
				return nil
			}
			for _, hit := range rawHTMLLines(data, needle) {
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
	data := []byte("<p>{text}</p>\n<div>{" + "@html a}</div>\n\n{" + "@html b}{" + "@html c}\n")
	got := fmt.Sprint(rawHTMLLines(data, []byte("{"+"@html")))
	if got != "[2 4]" {
		t.Fatalf("rawHTMLLines = %s, want [2 4]", got)
	}
}

// rawHTMLLines returns the 1-based numbers of the lines that contain needle.
func rawHTMLLines(data, needle []byte) []int {
	var lines []int
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), len(data)+1)
	for n := 1; scanner.Scan(); n++ {
		if bytes.Contains(scanner.Bytes(), needle) {
			lines = append(lines, n)
		}
	}
	return lines
}
