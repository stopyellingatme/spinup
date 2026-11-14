package render

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/templates"
)

func fragmentPack(name, content string) *templates.Pack {
	files := fstest.MapFS{}
	if content != "" {
		files["fragments/agents-rows.md.tmpl"] = &fstest.MapFile{Data: []byte(content)}
	}
	return &templates.Pack{Name: name, FS: files}
}

func TestFragmentsOrder(t *testing.T) {
	t.Parallel()
	packs := []*templates.Pack{
		fragmentPack("tofu", "| tofu | run | read |"),
		fragmentPack("web", "| web | run | read |"),
		fragmentPack("audit", "| audit | run | read |"),
		fragmentPack("api", "| api | run | read |"),
		fragmentPack("base", "| base | run | read |"),
	}
	want := "| base | run | read |\n| api | run | read |\n| web | run | read |\n| audit | run | read |\n| tofu | run | read |"
	first, err := newFragments(packs, nil, functions(nil))("agents-rows")
	if err != nil || first != want {
		t.Fatalf("first = %q, %v", first, err)
	}
	for i, j := 0, len(packs)-1; i < j; i, j = i+1, j-1 {
		packs[i], packs[j] = packs[j], packs[i]
	}
	second, err := newFragments(packs, nil, functions(nil))("agents-rows")
	if err != nil || second != first {
		t.Fatalf("second = %q, %v", second, err)
	}
}

func TestFragmentsMissingAndNames(t *testing.T) {
	t.Parallel()
	fragments := newFragments([]*templates.Pack{fragmentPack("base", "")}, nil, functions(nil))
	got, err := fragments("agents-rows")
	if err != nil || got != "" {
		t.Fatalf("missing = %q, %v", got, err)
	}
	for _, name := range []string{"Agents", "a b", ""} {
		_, err := fragments(name)
		if err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("name %q: %v", name, err)
		}
	}
}

func TestFragmentsInvalidRows(t *testing.T) {
	t.Parallel()
	for _, content := range []string{
		"| one | two |",
		"| one | two | three | four |",
		"not a row",
		"| one |  | three |",
	} {
		_, err := newFragments([]*templates.Pack{fragmentPack("base", content)}, nil, functions(nil))("agents-rows")
		if err == nil || !strings.Contains(err.Error(), "base") || !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("content %q: %v", content, err)
		}
	}
}

func TestFragmentsNormalizeAndFunctions(t *testing.T) {
	t.Parallel()
	p := fragmentPack("base", "\n| [[ .key ]] | [[ docurl \"build/live.md\" ]] | read |   \n\n")
	got, err := newFragments([]*templates.Pack{p}, map[string]any{"key": "live"}, functions(nil))("agents-rows")
	want := "| live | https://raw.githubusercontent.com/stopyellingatme/spinup/main/docs/build/live.md | read |"
	if err != nil || got != want || strings.HasSuffix(got, "\n") {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestFragmentsRejectsNestedCall(t *testing.T) {
	t.Parallel()
	_, err := newFragments([]*templates.Pack{fragmentPack("base", `[[ fragments "x" ]]`)}, nil, functions(nil))("agents-rows")
	if err == nil || !strings.Contains(err.Error(), "function \"fragments\" not defined") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("error: %v", err)
	}
}

func TestRenderFragments(t *testing.T) {
	t.Parallel()
	p := &templates.Pack{
		Name: "base",
		FS: fstest.MapFS{
			"fragments/agents-rows.md.tmpl": {Data: []byte("| row | run | read |")},
			"agents.md.tmpl":                {Data: []byte("rows:\n[[ fragments \"agents-rows\" ]]\n")},
		},
		Files: []templates.FileSpec{{Path: "AGENTS.md", Source: "agents.md.tmpl", Class: "managed"}},
	}
	first, err := Render(Input{Packs: []*templates.Pack{p}})
	if err != nil || len(first.Files) != 1 || string(first.Files[0].Data) != "rows:\n| row | run | read |\n" {
		t.Fatalf("first = %#v, %v", first.Files, err)
	}
	second, err := Render(Input{Packs: []*templates.Pack{p}})
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("second = %#v, %v", second, err)
	}
}

// TestFragmentsIgnorePluginPacks proves ADR 0035 section 4.5: a plugin pack with a
// fragment file adds no rows, and a malformed plugin fragment does not stop the render.
func TestFragmentsIgnorePluginPacks(t *testing.T) {
	t.Parallel()
	base := &templates.Pack{
		Name: "base",
		FS: fstest.MapFS{
			"fragments/agents-rows.md.tmpl": {Data: []byte("| base | run | read |")},
			"agents.md.tmpl":                {Data: []byte("[[ fragments \"agents-rows\" ]]\n")},
		},
		Files: []templates.FileSpec{{Path: "AGENTS.md", Source: "agents.md.tmpl", Class: "managed"}},
	}
	for _, content := range []string{
		"| plugin | run | read |",
		"not a row",
		"[[ .missing.field ]]",
		"[[ end ]]",
	} {
		plugin := &templates.Pack{
			Name: "acme-plugin",
			FS: fstest.MapFS{
				"fragments/agents-rows.md.tmpl": {Data: []byte(content)},
				"plugin.txt":                    {Data: []byte("plugin file\n")},
			},
			Files: []templates.FileSpec{{Path: "plugin.txt", Source: "plugin.txt", Class: "managed"}},
		}
		for _, packs := range [][]*templates.Pack{{base, plugin}, {plugin, base}} {
			plan, err := Render(Input{Packs: packs})
			if err != nil {
				t.Fatalf("content %q: %v", content, err)
			}
			agents, ok := plan.Get("AGENTS.md")
			if !ok || string(agents.Data) != "| base | run | read |\n" {
				t.Fatalf("content %q: AGENTS.md = %q", content, agents.Data)
			}
			if _, ok := plan.Get("plugin.txt"); !ok {
				t.Fatalf("content %q: the plugin file is missing from the plan", content)
			}
		}
		got, err := newFragments([]*templates.Pack{plugin}, nil, functions(nil))("agents-rows")
		if err != nil || got != "" {
			t.Fatalf("content %q: plugin rows = %q, %v", content, got, err)
		}
	}
}

func TestDocURL(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"build/live.md", "llms.txt"} {
		got, err := docurl(path)
		if err != nil || got != DocsBase+path {
			t.Fatalf("docurl(%q) = %q, %v", path, got, err)
		}
	}
	for _, path := range []string{"../x.md", "/x.md", "Build/x.md", "x.html", ""} {
		_, err := docurl(path)
		if err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Fatalf("docurl(%q): %v", path, err)
		}
	}
}
