package doctest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocsLint(t *testing.T) {
	problems, err := lintDocs(repositoryRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range problems {
		t.Error(problem.String())
	}
}

func TestDocsFacts(t *testing.T) {
	problems, err := lintFacts(repositoryRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range problems {
		t.Error(problem.String())
	}
}

func TestLintPage(t *testing.T) {
	root := t.TempDir()
	writeLintPage(t, root, "docs/other.md", "# Other page\n\n## Present heading\n")
	problems := lintPage(root, "docs/page.md", strings.Join([]string{
		"# Page",
		"[missing](missing.md)",
		"[anchor](other.md#missing-heading)",
		"[code](`missing.md`)",
		"```text",
		"[ignored](missing.md)",
		"| One | Two |",
		"| --- | --- |",
		"| one |",
	}, "\n"), map[string]map[string]bool{
		"docs/page.md":  {"page": true},
		"docs/other.md": {"other-page": true, "present-heading": true},
	})
	got := lintProblemStrings(problems)
	want := []string{
		"docs/page.md:2: link target \"missing.md\" does not exist",
		"docs/page.md:3: link anchor #missing-heading does not exist in docs/other.md",
		"docs/page.md:9: code fence is not closed",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lintPage() = %q, want %q", got, want)
	}
}

func TestLintLinkJekyllForms(t *testing.T) {
	root := t.TempDir()
	writeLintPage(t, root, "docs/guide/index.md", "# Guide\n")
	writeLintPage(t, root, "docs/page.md", "# Page\n")
	anchors := map[string]map[string]bool{
		"docs/guide/index.md": {"guide": true},
		"docs/page.md":        {"page": true},
	}
	for _, target := range []string{"guide", "guide/", "page.html", "#page"} {
		if problems := lintLink(root, "docs/page.md", 1, target, anchors); len(problems) != 0 {
			t.Errorf("lintLink(%q) = %v", target, problems)
		}
	}
}

func TestLintTables(t *testing.T) {
	problems := lintTables("docs/page.md", []string{
		"| One | Two |", "| --- | :---: |", "| one |", "", "| text |", "| --- |", "| one |",
	})
	got := lintProblemStrings(problems)
	want := []string{"docs/page.md:3: table row has 1 cells, want 2"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lintTables() = %q, want %q", got, want)
	}
}

func TestHeadingSlug(t *testing.T) {
	if got := headingSlug("Migration numbers"); got != "migration-numbers" {
		t.Fatalf("headingSlug() = %q", got)
	}
}

func TestLintRegressionCases(t *testing.T) {
	root := t.TempDir()
	writeLintPage(t, root, "README.md", "# Repository\n")
	anchors := map[string]map[string]bool{
		"docs/page.md": {"page": true},
	}
	for _, test := range []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "inline code heading anchor",
			run: func(t *testing.T) {
				if !pageAnchors("## `Hello` world\n")["hello-world"] {
					t.Fatal("inline code heading did not create hello-world")
				}
			},
		},
		{
			name: "repeated heading suffix",
			run: func(t *testing.T) {
				if !pageAnchors("## Same\n## Same\n")["same-1"] {
					t.Fatal("repeated heading did not create same-1")
				}
			},
		},
		{
			name: "reference link to missing file",
			run: func(t *testing.T) {
				problems := lintPage(root, "docs/page.md", "[text][ref]\n\n[ref]: missing.md\n", anchors)
				want := []string{"docs/page.md:1: link target \"missing.md\" does not exist"}
				if got := lintProblemStrings(problems); strings.Join(got, "\n") != strings.Join(want, "\n") {
					t.Fatalf("lintPage() = %q, want %q", got, want)
				}
			},
		},
		{
			name: "undefined reference is ignored",
			run: func(t *testing.T) {
				if got := lintPage(root, "docs/page.md", "[text][missing]\n[missing]\n", anchors); len(got) != 0 {
					t.Fatalf("lintPage() = %v, want no problems", got)
				}
			},
		},
		{
			name: "three space fence ignores links and tables",
			run: func(t *testing.T) {
				data := "   ```text\n[broken](missing.md)\n| One | Two |\n| --- | --- |\n| one |\n   ```\n"
				if got := lintPage(root, "docs/page.md", data, anchors); len(got) != 0 {
					t.Fatalf("lintPage() = %v, want no problems", got)
				}
			},
		},
		{
			name: "link to repository file",
			run: func(t *testing.T) {
				if got := lintPage(root, "docs/page.md", "[readme](../README.md)\n", anchors); len(got) != 0 {
					t.Fatalf("lintPage() = %v, want no problems", got)
				}
			},
		},
		{
			name: "link leaving repository",
			run: func(t *testing.T) {
				got := lintProblemStrings(lintPage(root, "docs/page.md", "[outside](../../README.md)\n", anchors))
				want := []string{"docs/page.md:1: link target \"../../README.md\" does not exist"}
				if strings.Join(got, "\n") != strings.Join(want, "\n") {
					t.Fatalf("lintPage() = %q, want %q", got, want)
				}
			},
		},
		{
			name: "findings after fence use source lines",
			run: func(t *testing.T) {
				data := strings.Join([]string{
					"# Page", "", "```text", "[ignored](missing.md)", "```", "", "[broken](missing.md)", "", "| One | Two |", "| --- | --- |", "| one |",
				}, "\n")
				got := lintProblemStrings(lintPage(root, "docs/page.md", data, anchors))
				want := []string{
					"docs/page.md:7: link target \"missing.md\" does not exist",
					"docs/page.md:11: table row has 1 cells, want 2",
				}
				if strings.Join(got, "\n") != strings.Join(want, "\n") {
					t.Fatalf("lintPage() = %q, want %q", got, want)
				}
			},
		},
	} {
		t.Run(test.name, test.run)
	}
}

func TestLintFacts(t *testing.T) {
	root := t.TempDir()
	writeLintPage(t, root, "docs/facts.md", "Go 1.26 or newer.\nGo is version 1.27 or later.\n")
	writeLintPage(t, root, "docs/adr/old.md", "Go 1.26 or newer.\n")
	problems, err := lintFacts(root)
	if err != nil {
		t.Fatal(err)
	}
	got := lintProblemStrings(problems)
	want := []string{"docs/facts.md:1: Go minimum is 1.27, not 1.26"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lintFacts() = %q, want %q", got, want)
	}
}

func writeLintPage(t *testing.T, root, path, data string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lintProblemStrings(problems []lintProblem) []string {
	strings := make([]string, len(problems))
	for index, problem := range problems {
		strings[index] = problem.String()
	}
	return strings
}
