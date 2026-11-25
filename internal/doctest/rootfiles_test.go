package doctest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	siteURLPrefix = "https://stopyellingatme.github.io/spinup/"
	blobURLPrefix = "https://github.com/stopyellingatme/spinup/blob/main/"
)

// rootMarkdownFiles are the markdown files outside docs/ that the guard checks.
var rootMarkdownFiles = []string{
	"README.md",
	"CONTRIBUTING.md",
	"SECURITY.md",
	"MAINTAINERS.md",
	"THIRD_PARTY_NOTICES.md",
	"CODE_OF_CONDUCT.md",
	".github/pull_request_template.md",
}

func TestDocsRootFiles(t *testing.T) {
	root := repositoryRoot(t)
	anchors := rootAnchors(t, root)
	contents := make(map[string]string)
	for _, page := range rootMarkdownFiles {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(page)))
		if err != nil {
			t.Errorf("%s:1: cannot read the file: %v", page, err)
			continue
		}
		contents[page] = normalizeLines(string(data))
		for _, problem := range lintRootPage(root, page, contents[page], anchors) {
			t.Error(problem.String())
		}
	}
	for _, problem := range lintReadmeGolden(root, contents["README.md"]) {
		t.Error(problem.String())
	}
	for _, problem := range lintReadmeCommit(root, contents["README.md"]) {
		t.Error(problem.String())
	}
	for _, problem := range lintLegalFiles(root) {
		t.Error(problem.String())
	}
	for _, problem := range lintVendorNotices(root, contents["THIRD_PARTY_NOTICES.md"]) {
		t.Error(problem.String())
	}
	for _, page := range []string{"README.md", "CONTRIBUTING.md"} {
		for _, problem := range lintMakeTargets(root, page, contents[page]) {
			t.Error(problem.String())
		}
	}
}

func TestLintRootFiles(t *testing.T) {
	root := t.TempDir()
	writeLintPage(t, root, "docs/index.md", "# Home\n")
	writeLintPage(t, root, "docs/a/b.md", "# B page\n\n## Real heading\n")
	writeLintPage(t, root, "docs/a/index.md", "# A section\n")
	writeLintPage(t, root, "docs/llms.txt", "# llms\n")
	writeLintPage(t, root, "docs/adr/0001-x.md", "# ADR\n")
	writeLintPage(t, root, "kit/doc.go", "package kit\n")
	anchors := map[string]map[string]bool{
		"docs/a/b.md": pageAnchors("# B page\n\n## Real heading\n"),
	}
	site := siteURLPrefix
	cases := []struct {
		name string
		link string
		want string
	}{
		{"site page", "[x](" + site + "a/b.html)", ""},
		{"site page anchor", "[x](" + site + "a/b.html#real-heading)", ""},
		{"site section", "[x](" + site + "a/)", ""},
		{"site home", "[x](" + site + ")", ""},
		{"site llms", "[x](" + site + "llms.txt)", ""},
		{"site missing", "[x](" + site + "a/missing.html)", "does not exist"},
		{"site missing section", "[x](" + site + "nope/)", "does not exist"},
		{"site bad anchor", "[x](" + site + "a/b.html#nope)", "anchor"},
		{"site adr", "[x](" + site + "adr/0001-x.html)", "not published"},
		{"site adr directory", "[x](" + site + "adr/)", "not published"},
		{"site reference", "[x][ref]\n\n[ref]: " + site + "a/missing.html", "does not exist"},
		{"blob good", "[x](" + blobURLPrefix + "kit/doc.go)", ""},
		{"blob directory", "[x](" + blobURLPrefix + "kit)", ""},
		{"blob missing", "[x](" + blobURLPrefix + "kit/gone.go)", "does not exist"},
		{"relative directory", "[x](kit/)", ""},
		{"relative missing", "[x](gone.md)", "does not exist"},
	}
	for _, tc := range cases {
		problems := lintRootPage(root, "README.md", tc.link+"\n", anchors)
		switch {
		case tc.want == "" && len(problems) > 0:
			t.Errorf("%s: unexpected problem: %s", tc.name, problems[0].String())
		case tc.want != "" && len(problems) == 0:
			t.Errorf("%s: want a problem containing %q, got none", tc.name, tc.want)
		case tc.want != "" && !strings.Contains(problems[0].Message, tc.want):
			t.Errorf("%s: problem %q does not contain %q", tc.name, problems[0].Message, tc.want)
		}
	}
}

func TestLintMakeTargets(t *testing.T) {
	root := t.TempDir()
	writeLintPage(t, root, "Makefile", ".PHONY: build test\nbuild: ## x\n\t@true\ntest:\n\t@true\n")
	data := "Run `make build test PKGS=./x/...` now.\nPlain text: make a branch.\n```sh\nmake -C spinup build # note\nmake nothing\n```\n"
	problems := lintMakeTargets(root, "README.md", data)
	if len(problems) != 1 || !strings.Contains(problems[0].Message, `"nothing"`) || problems[0].Line != 5 {
		t.Fatalf("problems = %v", problems)
	}
}

// normalizeLines turns CRLF into LF so that a Windows checkout compares equal.
func normalizeLines(data string) string {
	return strings.ReplaceAll(data, "\r\n", "\n")
}

func rootAnchors(t *testing.T, root string) map[string]map[string]bool {
	t.Helper()
	pages, err := lintPages(root)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			pages = append(pages, entry.Name())
		}
	}
	pages = append(pages, ".github/pull_request_template.md")
	anchors := make(map[string]map[string]bool, len(pages))
	for _, page := range pages {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(page)))
		if err != nil {
			continue
		}
		anchors[page] = pageAnchors(normalizeLines(string(data)))
	}
	return anchors
}

type rootLink struct {
	line int
	url  string
}

// rootLinks returns each link target of a page. Reference definitions count once, at their own line.
func rootLinks(data string) []rootLink {
	lines := markdownLines(data)
	var links []rootLink
	for index, line := range lines {
		if match := markdownReferencePattern.FindStringSubmatch(line); match != nil {
			links = append(links, rootLink{index + 1, strings.Trim(match[2], "<>")})
			continue
		}
		for _, link := range markdownLinks(line) {
			links = append(links, rootLink{index + 1, link})
		}
	}
	return links
}

func lintRootPage(root, page, data string, anchors map[string]map[string]bool) []lintProblem {
	data = normalizeLines(data)
	problems := lintPage(root, page, data, anchors)
	for _, link := range rootLinks(data) {
		switch {
		case strings.HasPrefix(link.url, siteURLPrefix):
			problems = append(problems, lintSiteLink(root, page, link, anchors)...)
		case strings.HasPrefix(link.url, blobURLPrefix):
			problems = append(problems, lintBlobLink(root, page, link)...)
		}
	}
	return problems
}

func lintSiteLink(root, page string, link rootLink, anchors map[string]map[string]bool) []lintProblem {
	rest := strings.TrimPrefix(link.url, siteURLPrefix)
	rest = strings.SplitN(rest, "?", 2)[0]
	path, anchor, _ := strings.Cut(rest, "#")
	fail := func(format string, args ...any) []lintProblem {
		return []lintProblem{{page, link.line, fmt.Sprintf(format, args...)}}
	}
	if first, _, _ := strings.Cut(path, "/"); first == "adr" || first == "design" {
		return fail("site link %q is not published: the site excludes docs/%s", link.url, first)
	}
	var target string
	switch {
	case path == "" || strings.HasSuffix(path, "/"):
		target = "docs/" + path + "index.md"
	case strings.HasSuffix(path, ".html"):
		target = "docs/" + strings.TrimSuffix(path, ".html") + ".md"
	case filepath.Ext(path) != "":
		target = "docs/" + path
	default:
		return fail("site link %q needs a .html suffix or a trailing slash", link.url)
	}
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(target)))
	if err != nil || info.IsDir() {
		if resolved, ok := resolvePage(root, "docs/"+strings.TrimSuffix(path, ".html")); !ok || resolved != target {
			return fail("site link %q does not exist: no file %s", link.url, target)
		}
	}
	if anchor == "" {
		return nil
	}
	pageAnchorSet, ok := anchors[target]
	if !ok {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(target)))
		if err != nil {
			return fail("site link %q: cannot read %s: %v", link.url, target, err)
		}
		pageAnchorSet = pageAnchors(normalizeLines(string(data)))
	}
	if !pageAnchorSet[anchor] {
		return fail("site link anchor #%s does not exist in %s", anchor, target)
	}
	return nil
}

func lintBlobLink(root, page string, link rootLink) []lintProblem {
	rest := strings.TrimPrefix(link.url, blobURLPrefix)
	rest = strings.SplitN(strings.SplitN(rest, "#", 2)[0], "?", 2)[0]
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(rest, "/")))); err != nil {
		return []lintProblem{{page, link.line, fmt.Sprintf("blob link %q does not exist: no path %s in the repository", link.url, rest)}}
	}
	return nil
}

var goldenMarker = regexp.MustCompile(`^<!-- golden: (\S+)#L(\d+)-L(\d+) -->$`)

func lintReadmeGolden(root, data string) []lintProblem {
	const page = "README.md"
	var problems []lintProblem
	lines := strings.Split(data, "\n")
	for index, line := range lines {
		match := goldenMarker.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			if strings.Contains(line, "<!-- golden:") {
				problems = append(problems, lintProblem{page, index + 1, "golden marker has the wrong form: want <!-- golden: <path>#L<a>-L<b> -->"})
			}
			continue
		}
		start, _ := strconv.Atoi(match[2])
		end, _ := strconv.Atoi(match[3])
		fail := func(format string, args ...any) {
			problems = append(problems, lintProblem{page, index + 1, fmt.Sprintf(format, args...)})
		}
		if index+1 >= len(lines) {
			fail("golden marker for %s has no code block after it", match[1])
			continue
		}
		marker, width, ok := lintFenceStart(lines[index+1])
		if !ok {
			fail("golden marker for %s is not followed by a fenced code block", match[1])
			continue
		}
		var block []string
		closed := false
		for _, body := range lines[index+2:] {
			if lintFenceEnd(body, marker, width) {
				closed = true
				break
			}
			block = append(block, body)
		}
		if !closed {
			fail("code block after the golden marker for %s is not closed", match[1])
			continue
		}
		file, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(match[1])))
		if err != nil {
			fail("golden file %s cannot be read: %v", match[1], err)
			continue
		}
		fileLines := strings.Split(strings.TrimSuffix(normalizeLines(string(file)), "\n"), "\n")
		if start < 1 || end < start || end > len(fileLines) {
			fail("golden range L%d-L%d is outside %s, which has %d lines", start, end, match[1], len(fileLines))
			continue
		}
		want := strings.Join(fileLines[start-1:end], "\n")
		if got := strings.Join(block, "\n"); got != want {
			fail("code block differs from lines %d to %d of %s:\n--- block\n%s\n--- file\n%s", start, end, match[1], got, want)
		}
	}
	return problems
}

var (
	readmeCommit   = regexp.MustCompile("(?i)commit `([0-9a-f]{12})`")
	baselineCommit = regexp.MustCompile(`(?m)^- Commit: ([0-9a-f]{40})\s*$`)
)

func lintReadmeCommit(root, data string) []lintProblem {
	const page = "README.md"
	baseline, err := os.ReadFile(filepath.Join(root, "perf", "baseline", "apple-m1-max.md"))
	if err != nil {
		return []lintProblem{{page, 1, fmt.Sprintf("cannot read the perf baseline: %v", err)}}
	}
	match := baselineCommit.FindStringSubmatch(normalizeLines(string(baseline)))
	if match == nil {
		return []lintProblem{{"perf/baseline/apple-m1-max.md", 1, "no line of the form \"- Commit: <40 hex>\""}}
	}
	var problems []lintProblem
	found := false
	for index, line := range strings.Split(data, "\n") {
		for _, named := range readmeCommit.FindAllStringSubmatch(line, -1) {
			found = true
			if !strings.HasPrefix(match[1], strings.ToLower(named[1])) {
				problems = append(problems, lintProblem{page, index + 1, fmt.Sprintf("commit %s differs from the baseline commit %s", named[1], match[1])})
			}
		}
	}
	if !found {
		problems = append(problems, lintProblem{page, 1, "the Performance section names no commit in the form: commit `<12 hex>`"})
	}
	return problems
}

func lintLegalFiles(root string) []lintProblem {
	var problems []lintProblem
	license, err := os.ReadFile(filepath.Join(root, "LICENSE"))
	if err != nil {
		problems = append(problems, lintProblem{"LICENSE", 1, fmt.Sprintf("cannot read the file: %v", err)})
	} else {
		text := normalizeLines(string(license))
		if !strings.HasPrefix(text, "MIT License") {
			problems = append(problems, lintProblem{"LICENSE", 1, `the file must start with "MIT License"`})
		}
		if !strings.Contains(text, "Copyright (c) 2026 Taylor King and the spinup authors") {
			problems = append(problems, lintProblem{"LICENSE", 3, `the file must contain "Copyright (c) 2026 Taylor King and the spinup authors"`})
		}
	}
	conduct, err := os.ReadFile(filepath.Join(root, "CODE_OF_CONDUCT.md"))
	if err != nil {
		problems = append(problems, lintProblem{"CODE_OF_CONDUCT.md", 1, fmt.Sprintf("cannot read the file: %v", err)})
	} else if strings.Contains(string(conduct), "INSERT CONTACT METHOD") {
		problems = append(problems, lintProblem{"CODE_OF_CONDUCT.md", 1, `the contact placeholder "INSERT CONTACT METHOD" is still there`})
	}
	return problems
}

func lintVendorNotices(root, notices string) []lintProblem {
	const directory = "internal/templates/packs/monitoring/files/monitoring/grafana/dashboards/stack/vendor"
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(directory)))
	if err != nil {
		return []lintProblem{{directory, 1, fmt.Sprintf("cannot read the vendor directory: %v", err)}}
	}
	var problems []lintProblem
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if !strings.Contains(notices, entry.Name()) {
			problems = append(problems, lintProblem{"THIRD_PARTY_NOTICES.md", 1, fmt.Sprintf("the vendored dashboard %s/%s is not named", directory, entry.Name())})
		}
	}
	return problems
}

var (
	makeCodeSpan   = regexp.MustCompile("`([^`]+)`")
	makeWord       = regexp.MustCompile(`\bmake\b`)
	makeTargetName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	makeRule       = regexp.MustCompile(`^([A-Za-z0-9_.-]+(?:\s+[A-Za-z0-9_.-]+)*)\s*:`)
)

func makefileTargets(root string) (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		return nil, err
	}
	targets := make(map[string]bool)
	for _, line := range strings.Split(normalizeLines(string(data)), "\n") {
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, ".") {
			continue
		}
		if strings.Contains(line, ":=") || strings.Contains(line, "?=") {
			continue
		}
		if match := makeRule.FindStringSubmatch(line); match != nil {
			for _, name := range strings.Fields(match[1]) {
				targets[name] = true
			}
		}
	}
	return targets, nil
}

func lintMakeTargets(root, page, data string) []lintProblem {
	targets, err := makefileTargets(root)
	if err != nil {
		return []lintProblem{{page, 1, fmt.Sprintf("cannot read the Makefile: %v", err)}}
	}
	var problems []lintProblem
	check := func(number int, code string) {
		for _, location := range makeWord.FindAllStringIndex(code, -1) {
			fields := strings.Fields(code[location[1]:])
			if len(fields) > 1 && fields[0] == "-C" {
				fields = fields[2:]
			}
			var names []string
			for _, field := range fields {
				if !makeTargetName.MatchString(field) {
					break
				}
				names = append(names, field)
			}
			if len(names) == 0 {
				continue
			}
			for _, name := range names {
				if !targets[name] {
					problems = append(problems, lintProblem{page, number, fmt.Sprintf("make target %q is not a target of the Makefile", name)})
				}
			}
		}
	}
	inFence := false
	var marker byte
	var width int
	for index, line := range strings.Split(data, "\n") {
		if inFence {
			if lintFenceEnd(line, marker, width) {
				inFence = false
				continue
			}
			check(index+1, line)
			continue
		}
		if nextMarker, nextWidth, ok := lintFenceStart(line); ok {
			inFence, marker, width = true, nextMarker, nextWidth
			continue
		}
		for _, span := range makeCodeSpan.FindAllStringSubmatch(line, -1) {
			check(index+1, span[1])
		}
	}
	sort.SliceStable(problems, func(i, j int) bool { return problems[i].Line < problems[j].Line })
	return problems
}
