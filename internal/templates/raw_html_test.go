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

// rawHTMLPattern is one raw-HTML escape hatch that a template must not use. The message of a hit
// is "<name>: <reason>" and then a Try: line.
type rawHTMLPattern struct {
	name   string
	re     *regexp.Regexp
	reason string
	try    string
}

// rawHTMLPatterns lists the escape hatches. Each row matches no file in the packs or in the
// generator templates today, so the test needs no allow-list.
//
// The innerHTML row matches the assignment form only (innerHTML={...}, innerHTML="...", and
// el.innerHTML = "..."). A bare read of the DOM property, such as in vendored code, is not a
// sink and must not match.
var rawHTMLPatterns = []rawHTMLPattern{
	{
		name:   "{@html",
		re:     regexp.MustCompile(`\{\s*@html\b`),
		reason: "{@html renders a string without escaping it, so user text becomes markup (XSS).",
		try:    "render the text with {value}, or build the markup with Svelte elements.",
	},
	{
		name:   "innerHTML=",
		re:     regexp.MustCompile(`\binnerHTML\s*=\s*[{"'` + "`" + `]`),
		reason: "innerHTML= (the Solid prop or a DOM assignment) renders a string without escaping it, so user text becomes markup (XSS).",
		try:    "render the text as a child expression, or build the markup with JSX elements.",
	},
	{
		name:   "dangerouslySetInnerHTML",
		re:     regexp.MustCompile(`\bdangerouslySetInnerHTML\b`),
		reason: "dangerouslySetInnerHTML renders a string without escaping it, so user text becomes markup (XSS).",
		try:    "render the text as a child expression, or build the markup with JSX elements.",
	},
}

// TestNoRawHTML fails for each raw-HTML escape hatch in a template pack or a generator template
// (ADR 0036 section 3.3). The frameworks escape text in braces. These escape hatches do not, so
// user text in them is an XSS.
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
			for _, p := range rawHTMLPatterns {
				for _, hit := range rawHTMLLines(p, data) {
					t.Errorf("%s:%d: %s\n  Try: %s", filepath.ToSlash(path), hit, p.reason, p.try)
				}
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

// TestRawHTMLLines checks that each pattern reports the line of each hit and skips the safe forms.
func TestRawHTMLLines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data string
		want string
	}{
		{"{@html", "<p>{text}</p>\n<div>{@html a}</div>\n\n{@html b}{@html c}\n{ @html d}\n{\n\t@html e}\n", "[2 4 4 5 6]"},
		{"innerHTML=", "<div innerHTML={a} />\n<p>{text}</p>\n<div innerHTML=\"x\" />\nel.innerHTML = 'y'\nconst h = el.innerHTML\nif (el.innerHTML === z) {}\n", "[1 3 4]"},
		{"dangerouslySetInnerHTML", "<p>{text}</p>\n<div dangerouslySetInnerHTML={{ __html: a }} />\n\n<div dangerouslySetInnerHTML={b} />\n", "[2 4]"},
	}
	byName := map[string]rawHTMLPattern{}
	for _, p := range rawHTMLPatterns {
		byName[p.name] = p
	}
	if len(byName) != len(tests) {
		t.Fatalf("%d patterns and %d cases. Each pattern needs a case.", len(byName), len(tests))
	}
	for _, tt := range tests {
		p, ok := byName[tt.name]
		if !ok {
			t.Fatalf("no pattern named %q", tt.name)
		}
		if got := fmt.Sprint(rawHTMLLines(p, []byte(tt.data))); got != tt.want {
			t.Errorf("%s: rawHTMLLines = %s, want %s", tt.name, got, tt.want)
		}
	}
}

// rawHTMLLines returns the 1-based numbers of the lines where a match of the pattern starts.
func rawHTMLLines(p rawHTMLPattern, data []byte) []int {
	matches := p.re.FindAllIndex(data, -1)
	lines := make([]int, 0, len(matches))
	for _, match := range matches {
		lines = append(lines, bytes.Count(data[:match[0]], []byte("\n"))+1)
	}
	return lines
}
