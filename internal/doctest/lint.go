package doctest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/stopyellingatme/spinup/internal/stackversions"
)

type lintProblem struct {
	Path    string
	Line    int
	Message string
}

func (p lintProblem) String() string {
	return fmt.Sprintf("%s:%d: %s", p.Path, p.Line, p.Message)
}

var goFactPattern = regexp.MustCompile(`(?i)\bGo(?: is)?(?: version)? ([0-9]+\.[0-9]+) or (?:newer|later)\b`)

func lintDocs(root string) ([]lintProblem, error) {
	pages, err := lintPages(root)
	if err != nil {
		return nil, err
	}
	anchors := make(map[string]map[string]bool, len(pages))
	for _, page := range pages {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(page)))
		if err != nil {
			return nil, err
		}
		anchors[page] = pageAnchors(string(data))
	}

	var problems []lintProblem
	for _, page := range pages {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(page)))
		if err != nil {
			return nil, err
		}
		problems = append(problems, lintPage(root, page, string(data), anchors)...)
	}
	sort.Slice(problems, func(i, j int) bool {
		if problems[i].Path != problems[j].Path {
			return problems[i].Path < problems[j].Path
		}
		if problems[i].Line != problems[j].Line {
			return problems[i].Line < problems[j].Line
		}
		return problems[i].Message < problems[j].Message
	})
	return problems, nil
}

func lintFacts(root string) ([]lintProblem, error) {
	stack, err := stackversions.Load()
	if err != nil {
		return nil, err
	}
	pages, err := lintPages(root)
	if err != nil {
		return nil, err
	}
	var problems []lintProblem
	for _, page := range pages {
		if strings.HasPrefix(page, "docs/adr/") || strings.HasPrefix(page, "docs/design/") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(page)))
		if err != nil {
			return nil, err
		}
		for line, text := range markdownLines(string(data)) {
			for _, match := range goFactPattern.FindAllStringSubmatch(text, -1) {
				if match[1] != stack.Go.Min {
					problems = append(problems, lintProblem{page, line + 1, fmt.Sprintf("Go minimum is %s, not %s", stack.Go.Min, match[1])})
				}
			}
		}
	}
	return problems, nil
}

func lintPages(root string) ([]string, error) {
	docs := filepath.Join(root, "docs")
	var pages []string
	err := filepath.WalkDir(docs, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && skipLintDirectory(path, entry.Name()) {
			return filepath.SkipDir
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			pages = append(pages, filepath.ToSlash(relative))
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	sort.Strings(pages)
	return pages, err
}

func skipLintDirectory(path, name string) bool {
	if filepath.ToSlash(path) == "docs" {
		return false
	}
	return name == "site" || name == "_site" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

func lintPage(root, page, data string, anchors map[string]map[string]bool) []lintProblem {
	lines := strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n")
	var problems []lintProblem
	inFence := false
	var marker byte
	var width int
	for index, line := range lines {
		if !inFence {
			if nextMarker, nextWidth, ok := fenceStart(line); ok {
				inFence, marker, width = true, nextMarker, nextWidth
				continue
			}
			for _, link := range markdownLinks(line) {
				problems = append(problems, lintLink(root, page, index+1, link, anchors)...)
			}
			continue
		}
		if fenceEnd(line, marker, width) {
			inFence = false
		}
	}
	if inFence {
		problems = append(problems, lintProblem{page, len(lines), "code fence is not closed"})
	}
	problems = append(problems, lintTables(page, markdownLines(data))...)
	return problems
}

func pageAnchors(data string) map[string]bool {
	anchors := make(map[string]bool)
	for _, line := range markdownLines(data) {
		text := strings.TrimLeft(line, " ")
		level := 0
		for level < len(text) && text[level] == '#' {
			level++
		}
		if level == 0 || level == len(text) || text[level] != ' ' {
			continue
		}
		heading := strings.TrimSpace(text[level:])
		heading = strings.TrimSpace(strings.TrimRight(heading, "#"))
		anchors[headingSlug(heading)] = true
	}
	return anchors
}

func headingSlug(heading string) string {
	var slug strings.Builder
	dash := false
	for _, char := range strings.ToLower(heading) {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char > 127 && (char >= 'À' && char <= 'ÿ'):
			if dash && slug.Len() > 0 {
				slug.WriteByte('-')
			}
			slug.WriteRune(char)
			dash = false
		case char == ' ' || char == '-' || char == '_':
			if slug.Len() > 0 {
				dash = true
			}
		}
	}
	return slug.String()
}

func markdownLines(data string) []string {
	lines := strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n")
	var result []string
	inFence := false
	var marker byte
	var width int
	for _, line := range lines {
		if !inFence {
			if nextMarker, nextWidth, ok := fenceStart(line); ok {
				inFence, marker, width = true, nextMarker, nextWidth
				continue
			}
			result = append(result, stripInlineCode(line))
			continue
		}
		if fenceEnd(line, marker, width) {
			inFence = false
		}
		result = append(result, "")
	}
	return result
}

func markdownLinks(line string) []string {
	line = stripInlineCode(line)
	var links []string
	for index := 0; index < len(line); index++ {
		if line[index] != '[' || index > 0 && line[index-1] == '\\' {
			continue
		}
		close := strings.IndexByte(line[index:], ']')
		if close < 0 {
			continue
		}
		close += index
		if close+1 >= len(line) || line[close+1] != '(' {
			continue
		}
		end := close + 2
		depth := 1
		for ; end < len(line); end++ {
			if line[end] == '(' {
				depth++
			}
			if line[end] == ')' {
				depth--
				if depth == 0 {
					break
				}
			}
		}
		if depth != 0 {
			continue
		}
		target := strings.TrimSpace(line[close+2 : end])
		if space := strings.IndexAny(target, " \t"); space >= 0 {
			target = target[:space]
		}
		links = append(links, strings.Trim(target, "<>"))
		index = end
	}
	return links
}

func stripInlineCode(line string) string {
	var result strings.Builder
	for index := 0; index < len(line); {
		if line[index] != '`' {
			result.WriteByte(line[index])
			index++
			continue
		}
		end := strings.IndexByte(line[index+1:], '`')
		if end < 0 {
			return result.String()
		}
		index += end + 2
	}
	return result.String()
}

func lintLink(root, page string, line int, target string, anchors map[string]map[string]bool) []lintProblem {
	if target == "" || strings.HasPrefix(target, "/") || strings.Contains(target, ":") {
		return nil
	}
	target = strings.SplitN(target, "?", 2)[0]
	file, anchor, _ := strings.Cut(target, "#")
	targetPage := page
	if file != "" {
		resolved, ok := resolvePage(root, filepath.ToSlash(filepath.Join(filepath.Dir(page), file)))
		if !ok {
			return []lintProblem{{page, line, fmt.Sprintf("link target %q does not exist", file)}}
		}
		targetPage = resolved
	}
	if anchor != "" && !anchors[targetPage][anchor] {
		return []lintProblem{{page, line, fmt.Sprintf("link anchor #%s does not exist in %s", anchor, targetPage)}}
	}
	return nil
}

func resolvePage(root, target string) (string, bool) {
	target = filepath.ToSlash(filepath.Clean(target))
	if !strings.HasPrefix(target, "docs/") {
		return "", false
	}
	candidates := []string{target}
	if filepath.Ext(target) == "" {
		candidates = append(candidates, target+".md", target+"/index.md")
	}
	if strings.HasSuffix(target, ".html") {
		candidates = append(candidates, strings.TrimSuffix(target, ".html")+".md")
	}
	for _, candidate := range candidates {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(candidate)))
		if err == nil {
			if info.IsDir() {
				return candidate, true
			}
			return candidate, true
		}
	}
	return "", false
}

func lintTables(page string, lines []string) []lintProblem {
	var problems []lintProblem
	for index := 0; index+1 < len(lines); index++ {
		if !tableDelimiter(lines[index+1]) {
			continue
		}
		header := tableCells(lines[index])
		if len(header) == 0 {
			continue
		}
		for row := index + 2; row < len(lines); row++ {
			cells := tableCells(lines[row])
			if len(cells) == 0 {
				break
			}
			if len(cells) != len(header) {
				problems = append(problems, lintProblem{page, row + 1, fmt.Sprintf("table row has %d cells, want %d", len(cells), len(header))})
			}
		}
	}
	return problems
}

func tableDelimiter(line string) bool {
	cells := tableCells(line)
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		cell = strings.Trim(strings.TrimSpace(cell), ":")
		if len(cell) < 3 || strings.Trim(cell, "-") != "" {
			return false
		}
	}
	return true
}

func tableCells(line string) []string {
	if !strings.Contains(line, "|") {
		return nil
	}
	var cells []string
	var cell strings.Builder
	escaped := false
	for _, char := range line {
		switch {
		case escaped:
			cell.WriteRune(char)
			escaped = false
		case char == '\\':
			escaped = true
		case char == '|':
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
		default:
			cell.WriteRune(char)
		}
	}
	cells = append(cells, strings.TrimSpace(cell.String()))
	if strings.TrimSpace(line) == "" || len(cells) < 2 {
		return nil
	}
	if cells[0] == "" {
		cells = cells[1:]
	}
	if len(cells) > 0 && cells[len(cells)-1] == "" {
		cells = cells[:len(cells)-1]
	}
	return cells
}
