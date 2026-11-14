package docsite

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/stopyellingatme/spinup/internal/render"
)

const (
	llmsTitle   = "# spinup"
	llmsSummary = "> spinup creates and operates a full-stack app: a Go API, a SvelteKit web app, PostgreSQL, a deploy to a VM, and monitoring. It upgrades the app and keeps your edits."
	llmsNote    = "Each link points to a Markdown page. Read \"Get started\" first. The section \"Optional\" is for people who work on spinup itself."
)

var markdownLink = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)

type llmsPage struct {
	path        string
	title       string
	parent      string
	navOrder    int
	description string
}

// LLMS builds the llms.txt index from the supplied docs directory.
func LLMS(docs fs.FS) ([]byte, error) {
	pages, err := llmsPages(docs)
	if err != nil {
		return nil, err
	}
	sections := llmsSections(pages)
	var out strings.Builder
	out.WriteString(llmsTitle)
	out.WriteString("\n\n")
	out.WriteString(llmsSummary)
	out.WriteString("\n\n")
	out.WriteString(llmsNote)
	for index, section := range sections {
		if index == 0 {
			out.WriteString("\n\n")
		} else {
			out.WriteString("\n")
		}
		out.WriteString("## ")
		out.WriteString(section.name)
		out.WriteString("\n\n")
		for _, page := range section.pages {
			fmt.Fprintf(&out, "- [%s](%s%s)", page.title, render.DocsBase, page.path)
			if page.description != "" {
				fmt.Fprintf(&out, ": %s", page.description)
			}
			out.WriteString("\n")
		}
	}
	return []byte(out.String()), nil
}

func llmsPages(docs fs.FS) ([]llmsPage, error) {
	var pages []llmsPage
	err := fs.WalkDir(docs, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name == "adr" || name == "design" || name == "site" {
				return fs.SkipDir
			}
			return nil
		}
		if path.Ext(name) != ".md" || name == "index.md" {
			return nil
		}
		data, err := fs.ReadFile(docs, name)
		if err != nil {
			return err
		}
		page, excluded, err := parseLLMSPage(name, string(data))
		if err != nil {
			return err
		}
		if !excluded {
			pages = append(pages, page)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return pages, nil
}

func parseLLMSPage(name, data string) (llmsPage, bool, error) {
	metadata, body := llmsFrontMatter(data)
	if metadata["title"] == "" {
		return llmsPage{}, false, fmt.Errorf("cannot build llms.txt: %s has no title. Try: add title to the front matter", name)
	}
	if metadata["nav_exclude"] == "true" {
		return llmsPage{}, true, nil
	}
	order := 1000
	if text := metadata["nav_order"]; text != "" {
		if value, err := strconv.Atoi(text); err == nil {
			order = value
		}
	}
	return llmsPage{
		path:        name,
		title:       metadata["title"],
		parent:      metadata["parent"],
		navOrder:    order,
		description: llmsDescription(body),
	}, false, nil
}

func llmsFrontMatter(data string) (map[string]string, string) {
	lines := strings.Split(data, "\n")
	first, second := -1, -1
	for index, line := range lines {
		if strings.TrimSuffix(line, "\r") != "---" {
			continue
		}
		if first < 0 {
			first = index
		} else {
			second = index
			break
		}
	}
	metadata := map[string]string{}
	if first < 0 || second < 0 {
		return metadata, data
	}
	for _, line := range lines[first+1 : second] {
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		metadata[strings.TrimSpace(key)] = value
	}
	return metadata, strings.Join(lines[second+1:], "\n")
}

func llmsDescription(body string) string {
	lines := strings.Split(body, "\n")
	foundH1 := false
	fence := ""
	for index := 0; index < len(lines); {
		line := strings.TrimSpace(lines[index])
		if !foundH1 {
			if strings.HasPrefix(line, "# ") {
				foundH1 = true
			}
			index++
			continue
		}
		if fence != "" {
			if llmsClosesFence(line, fence) {
				fence = ""
			}
			index++
			continue
		}
		if open := llmsFenceRun(line); open != "" {
			fence = open
			index++
			continue
		}
		if !llmsProse(line) {
			index++
			continue
		}
		paragraph := []string{line}
		for index++; index < len(lines) && llmsProse(strings.TrimSpace(lines[index])); index++ {
			paragraph = append(paragraph, strings.TrimSpace(lines[index]))
		}
		return llmsShorten(strings.Join(paragraph, " "))
	}
	return ""
}

// llmsFenceRun returns the run of backticks or tildes that opens a code fence, or "".
// A fence has at least three of the same character.
func llmsFenceRun(line string) string {
	if line == "" || (line[0] != '`' && line[0] != '~') {
		return ""
	}
	run := len(line) - len(strings.TrimLeft(line, line[:1]))
	if run < 3 {
		return ""
	}
	return line[:run]
}

// llmsClosesFence reports whether line closes the code fence that open started.
// The closing run uses the same character, is at least as long, and has no other text.
func llmsClosesFence(line, open string) bool {
	run := llmsFenceRun(line)
	return run != "" && run[0] == open[0] && len(run) >= len(open) && strings.TrimSpace(line[len(run):]) == ""
}

func llmsProse(line string) bool {
	if line == "" || llmsFenceRun(line) != "" || strings.HasPrefix(line, "|") || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "<") || strings.HasPrefix(line, "{:") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "*") || strings.HasPrefix(line, ">") {
		return false
	}
	for index, runeValue := range line {
		if unicode.IsDigit(runeValue) {
			continue
		}
		return index == 0 || runeValue != '.'
	}
	return false
}

func llmsShorten(text string) string {
	text = markdownLink.ReplaceAllString(text, "$1")
	text = strings.Join(strings.Fields(text), " ")
	if sentence, _, found := strings.Cut(text, ". "); found {
		text = sentence + "."
	}
	if len(text) <= 200 {
		return text
	}
	text = text[:200]
	if index := strings.LastIndexByte(text, ' '); index > 0 {
		return text[:index]
	}
	return text
}

type llmsSection struct {
	name  string
	pages []llmsPage
}

func llmsSections(pages []llmsPage) []llmsSection {
	byName := map[string][]llmsPage{}
	parentOrder := map[string]int{}
	for _, page := range pages {
		section := page.parent
		if section == "" {
			section = "Overview"
			parentOrder[page.title] = page.navOrder
		}
		byName[section] = append(byName[section], page)
	}
	sections := make([]llmsSection, 0, len(byName))
	for name, sectionPages := range byName {
		if name == "Contributing" {
			name = "Optional"
		}
		sort.Slice(sectionPages, func(i, j int) bool {
			if sectionPages[i].navOrder != sectionPages[j].navOrder {
				return sectionPages[i].navOrder < sectionPages[j].navOrder
			}
			if sectionPages[i].title != sectionPages[j].title {
				return sectionPages[i].title < sectionPages[j].title
			}
			return sectionPages[i].path < sectionPages[j].path
		})
		sections = append(sections, llmsSection{name: name, pages: sectionPages})
	}
	sort.Slice(sections, func(i, j int) bool {
		if sections[i].name == "Overview" || sections[j].name == "Optional" {
			return true
		}
		if sections[j].name == "Overview" || sections[i].name == "Optional" {
			return false
		}
		left, leftOK := parentOrder[sections[i].name]
		right, rightOK := parentOrder[sections[j].name]
		if leftOK && rightOK && left != right {
			return left < right
		}
		if leftOK != rightOK {
			return leftOK
		}
		return sections[i].name < sections[j].name
	})
	return sections
}
