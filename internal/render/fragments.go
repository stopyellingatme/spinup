package render

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/template"

	"github.com/stopyellingatme/spinup/internal/templates"
)

// DocsBase is the raw Markdown base URL for the spinup documentation.
const DocsBase = "https://raw.githubusercontent.com/stopyellingatme/spinup/main/docs/"

var (
	fragmentName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	docPath      = regexp.MustCompile(`^[a-z0-9][a-z0-9/_-]*\.(md|txt)$`)
)

func docurl(path string) (string, error) {
	if !docPath.MatchString(path) || strings.Contains(path, "..") {
		return "", fmt.Errorf("doc URL path %q is invalid\nTry: use a docs-relative lowercase .md or .txt path", path)
	}
	return DocsBase + path, nil
}

// builtinPacks returns the names of the packs that ship with spinup. A plugin pack name
// never equals one of them, because the plugin overlay rejects a name that collides.
var builtinPacks = sync.OnceValue(func() map[string]bool {
	names, err := templates.List(templates.Embedded())
	if err != nil {
		panic(err)
	}
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
})

// fragmentProviders returns the built-in packs of a render. Plugin packs add no
// fragments (ADR 0035 section 4.5), so a plugin fragment file is never read.
func fragmentProviders(packs []*templates.Pack) []*templates.Pack {
	builtin := builtinPacks()
	providers := make([]*templates.Pack, 0, len(packs))
	for _, pack := range packs {
		if builtin[pack.Name] {
			providers = append(providers, pack)
		}
	}
	return providers
}

func newFragments(packs []*templates.Pack, data map[string]any, base template.FuncMap) func(string) (string, error) {
	ordered := fragmentProviders(packs)
	sort.SliceStable(ordered, func(i, j int) bool {
		return fragmentPackOrder(ordered[i].Name) < fragmentPackOrder(ordered[j].Name)
	})
	cache := map[string]fragmentResult{}
	return func(name string) (string, error) {
		if cached, ok := cache[name]; ok {
			return cached.text, cached.err
		}
		result := fragmentResult{}
		if !fragmentName.MatchString(name) {
			result.err = fmt.Errorf("fragment name %q is invalid\nTry: use lowercase letters, digits, and hyphens", name)
			cache[name] = result
			return result.text, result.err
		}
		result.text, result.err = renderFragments(ordered, name, data, base)
		cache[name] = result
		return result.text, result.err
	}
}

type fragmentResult struct {
	text string
	err  error
}

func fragmentPackOrder(name string) string {
	switch name {
	case "base":
		return "0"
	case "api":
		return "1"
	case "web":
		return "2"
	default:
		return "3" + name
	}
}

func renderFragments(packs []*templates.Pack, name string, data map[string]any, base template.FuncMap) (string, error) {
	var rows []string
	for _, pack := range packs {
		source := "fragments/" + name + ".md.tmpl"
		content, err := fs.ReadFile(pack.FS, source)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read fragment %s/%s: %w\nTry: provide a readable fragment file", pack.Name, source, err)
		}
		output, err := execute(pack.Name+"/"+source, string(content), data, base)
		if err != nil {
			return "", err
		}
		for lineNumber, line := range strings.Split(string(output), "\n") {
			line = strings.TrimRight(line, " \t")
			if strings.TrimSpace(line) == "" {
				continue
			}
			if err := validateFragmentRow(line); err != nil {
				return "", fmt.Errorf("fragment %s/%s line %d: %w\nTry: use a three-cell Markdown table row", pack.Name, source, lineNumber+1, err)
			}
			rows = append(rows, line)
		}
	}
	return strings.Join(rows, "\n"), nil
}

func validateFragmentRow(line string) error {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "| ") || !strings.HasSuffix(trimmed, " |") {
		return fmt.Errorf("is not a table row")
	}
	cells := strings.Split(strings.TrimSuffix(strings.TrimPrefix(trimmed, "| "), " |"), " | ")
	if len(cells) != 3 {
		return fmt.Errorf("has %d cells", len(cells))
	}
	for _, cell := range cells {
		if strings.TrimSpace(cell) == "" || strings.Contains(cell, "|") {
			return fmt.Errorf("has an invalid cell")
		}
	}
	return nil
}
