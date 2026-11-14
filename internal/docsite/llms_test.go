package docsite

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/render"
)

func TestLLMSOrderAndFixedText(t *testing.T) {
	t.Parallel()
	docs := fstest.MapFS{
		"index.md":              {Data: []byte("---\ntitle: Home\n---\n\n# Home\n\nSkip me.\n")},
		"get-started/index.md":  {Data: page("Get started", "", "2", "Start here.")},
		"get-started/second.md": {Data: page("Second", "Get started", "2", "Second page.")},
		"get-started/first.md":  {Data: page("First", "Get started", "1", "First page.")},
		"build/index.md":        {Data: page("Build", "", "3", "Build it.")},
		"build/guide.md":        {Data: page("Guide", "Build", "", "Guide page.")},
		"contributing/index.md": {Data: page("Contributing", "", "1", "Contribute.")},
		"contributing/guide.md": {Data: page("Writing", "Contributing", "", "Write docs.")},
	}
	got, err := LLMS(docs)
	if err != nil {
		t.Fatal(err)
	}
	want := "# spinup\n\n> spinup creates and operates a full-stack app: a Go API, a SvelteKit web app, PostgreSQL, a deploy to a VM, and monitoring. It upgrades the app and keeps your edits.\n\nEach link points to a Markdown page. Read \"Get started\" first. The section \"Optional\" is for people who work on spinup itself.\n\n## Overview\n\n- [Contributing](" + render.DocsBase + "contributing/index.md): Contribute.\n- [Get started](" + render.DocsBase + "get-started/index.md): Start here.\n- [Build](" + render.DocsBase + "build/index.md): Build it.\n\n## Get started\n\n- [First](" + render.DocsBase + "get-started/first.md): First page.\n- [Second](" + render.DocsBase + "get-started/second.md): Second page.\n\n## Build\n\n- [Guide](" + render.DocsBase + "build/guide.md): Guide page.\n\n## Optional\n\n- [Writing](" + render.DocsBase + "contributing/guide.md): Write docs.\n"
	if string(got) != want {
		t.Fatalf("LLMS() =\n%s\nwant:\n%s", got, want)
	}
}

func TestLLMSSkipsAndRequiresTitle(t *testing.T) {
	t.Parallel()
	docs := fstest.MapFS{
		"visible.md":  {Data: page("Visible", "", "", "Visible page.")},
		"hidden.md":   {Data: []byte("---\ntitle: Hidden\nnav_exclude: true\n---\n\n# Hidden\n\nHidden page.\n")},
		"adr/a.md":    {Data: page("ADR", "", "", "ADR page.")},
		"design/a.md": {Data: page("Design", "", "", "Design page.")},
		"site/a.md":   {Data: page("Site", "", "", "Site page.")},
	}
	got, err := LLMS(docs)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "Visible") || strings.Contains(text, "Hidden") || strings.Contains(text, "ADR") || strings.Contains(text, "Design") || strings.Contains(text, "Site") {
		t.Fatalf("LLMS() = %q", text)
	}
	_, err = LLMS(fstest.MapFS{"missing.md": {Data: []byte("---\nparent: Test\n---\n\n# Missing\n\nText.\n")}})
	if err == nil || !strings.Contains(err.Error(), "missing.md") || !strings.HasSuffix(err.Error(), "Try: add title to the front matter") {
		t.Fatalf("LLMS() error = %v", err)
	}
}

func TestLLMSDescriptions(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("word ", 45) + "end."
	docs := fstest.MapFS{
		"table.md": {Data: []byte("---\ntitle: Table\n---\n\n# Table\n\n| A | B |\n| - | - |\n\nText after table. More text.\n")},
		"code.md":  {Data: []byte("---\ntitle: Code\n---\n\n# Code\n\n```go\nfmt.Println()\n```\n\nText after code. More text.\n")},
		"link.md":  {Data: page("Link", "", "", "Read [the guide](guide.md) now. More text.")},
		"long.md":  {Data: page("Long", "", "", long)},
		"none.md":  {Data: []byte("---\ntitle: None\n---\n\n# None\n\n- List only\n")},
	}
	got, err := LLMS(docs)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, want := range []string{"Text after table.", "Text after code.", "Read the guide now.", "- [None](" + render.DocsBase + "none.md)\n"} {
		if !strings.Contains(text, want) {
			t.Fatalf("LLMS() does not contain %q:\n%s", want, text)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "[Long]") && len(strings.TrimPrefix(line, "- [Long]("+render.DocsBase+"long.md): ")) > 200 {
			t.Fatalf("long description is too long: %q", line)
		}
	}
}

func TestLLMSDeterministic(t *testing.T) {
	t.Parallel()
	docs := fstest.MapFS{
		"z.md": {Data: page("Same", "", "", "Zed.")},
		"a.md": {Data: page("Same", "", "", "Aye.")},
	}
	first, err := LLMS(docs)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LLMS(docs)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || strings.Index(string(first), "a.md") > strings.Index(string(first), "z.md") {
		t.Fatalf("LLMS is not deterministic: %s", first)
	}
}

func page(title, parent, order, prose string) []byte {
	var front strings.Builder
	front.WriteString("---\n")
	front.WriteString("title: \"")
	front.WriteString(title)
	front.WriteString("\"\n")
	if parent != "" {
		front.WriteString("parent: ")
		front.WriteString(parent)
		front.WriteString("\n")
	}
	if order != "" {
		front.WriteString("nav_order: ")
		front.WriteString(order)
		front.WriteString("\n")
	}
	front.WriteString("---\n\n# ")
	front.WriteString(title)
	front.WriteString("\n\n")
	front.WriteString(prose)
	front.WriteString("\n")
	return []byte(front.String())
}
