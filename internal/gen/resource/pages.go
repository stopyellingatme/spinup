package resource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/fields"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// The pages part writes the SvelteKit pages of a resource when the app has
// the web pack (ADR 0008 §4, ADR 0015 §5). The pages call the Go API through
// the typed client, so the part requests the client post-step.
func init() {
	RegisterPart(Part{Name: "pages", Plan: planPages, Paths: pagePaths})
}

// pagesDir returns the route directory of a resource, such as
// web/src/routes/line-items.
func pagesDir(name fields.Name) string { return "web/src/routes/" + name.PluralKebab() }

// pageFiles maps the files of the pages part, relative to pagesDir, to their
// templates.
var pageFiles = []struct{ path, source string }{
	{"+page.server.ts", listServerTS},
	{"+page.svelte", listSvelte},
	{"[id]/+page.server.ts", itemServerTS},
	{"[id]/+page.svelte", itemSvelte},
	{"[id]/edit/+page.server.ts", editServerTS},
	{"[id]/edit/+page.svelte", editSvelte},
	{"fields.svelte", fieldsSvelte},
	{"new/+page.server.ts", newServerTS},
	{"new/+page.svelte", newSvelte},
	{"schema.ts", schemaTS},
}

func pagePaths(name fields.Name) []string {
	paths := make([]string, 0, len(pageFiles))
	for _, f := range pageFiles {
		paths = append(paths, pagesDir(name)+"/"+f.path)
	}
	return paths
}

// hasWeb reports whether the lock at root lists the web pack. A project
// without a lock has no web pack.
func hasWeb(root string) (bool, error) {
	lock, err := manifest.Load(filepath.Join(root, "spinup.lock"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return slices.Contains(lock.Packs, "web"), nil
}

func planPages(_ context.Context, spec Spec) (vfs.Plan, []gen.PostStep, error) {
	web, err := hasWeb(spec.Root)
	if err != nil || !web {
		return vfs.Plan{}, nil, err
	}
	lock, err := manifest.Load(filepath.Join(spec.Root, "spinup.lock"))
	if err != nil {
		return vfs.Plan{}, nil, err
	}
	if lock.Answers["frontend_mode"] == "embed" {
		return vfs.Plan{}, nil, fmt.Errorf("cannot generate resource pages in embed mode: the pages require SvelteKit form actions\nTry: pass --api-only, or use an app with --frontend-mode node")
	}
	if err := checkPageInputs(spec.Root); err != nil {
		return vfs.Plan{}, nil, err
	}
	data := newPageData(spec)
	data.Auth = slices.Contains(lock.Packs, "auth")
	var plan vfs.Plan
	for _, f := range pageFiles {
		out, err := renderPage(f.path, f.source, data)
		if err != nil {
			return vfs.Plan{}, nil, err
		}
		if err := plan.Add(vfs.File{Path: pagesDir(spec.Name) + "/" + f.path, Data: out, Class: string(manifest.Scaffold)}); err != nil {
			return vfs.Plan{}, nil, err
		}
	}
	return plan, []gen.PostStep{gen.PostClient}, nil
}

// The files and npm packages that the pages import. Apps made before M12 lack
// them until spinup upgrade adds the new web pack files (ADR 0015 §5).
var (
	pageComponents = []string{"alert-dialog", "badge", "button", "card", "checkbox", "dropdown-menu", "input", "label", "pagination", "table"}
	pageAppFiles   = []string{"web/src/lib/components/app/field.svelte", "web/src/lib/components/app/page-header.svelte"}
	pagePackages   = []string{"sveltekit-superforms", "zod", "svelte-sonner"}
)

// checkPageInputs fails when the app lacks a file or an npm package that the
// pages import, and names each one.
func checkPageInputs(root string) error {
	var missing []string
	exists := func(rel string) (bool, error) {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("check %s: %w\nTry: check the file permissions", rel, err)
		}
		return true, nil
	}
	var paths []string
	for _, name := range pageComponents {
		paths = append(paths, "web/src/lib/components/ui/"+name+"/")
	}
	for _, rel := range append(paths, pageAppFiles...) {
		ok, err := exists(rel)
		if err != nil {
			return err
		}
		if !ok {
			missing = append(missing, rel)
		}
	}
	deps, err := webDependencies(root)
	if err != nil {
		return err
	}
	for _, name := range pagePackages {
		if !deps[name] {
			missing = append(missing, "the npm package "+name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("cannot generate resource pages: the app is missing %s\nTry: spinup upgrade (it adds the new web pack files), then run the generator again", strings.Join(missing, ", "))
}

// webDependencies returns the names of the dependencies and dev dependencies
// in web/package.json. A missing file has none.
func webDependencies(root string) (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(root, "web", "package.json"))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read web/package.json: %w\nTry: check the file permissions", err)
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("read web/package.json: %w\nTry: fix the JSON syntax of web/package.json", err)
	}
	deps := map[string]bool{}
	for name := range pkg.Dependencies {
		deps[name] = true
	}
	for name := range pkg.DevDependencies {
		deps[name] = true
	}
	return deps, nil
}

func renderPage(name, source string, data pageData) ([]byte, error) {
	t, err := template.New(name).Delims("[[", "]]").Option("missingkey=error").Funcs(template.FuncMap{"indent": indent}).Parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse the page template %s: %w\nTry: report this generator bug", name, err)
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return nil, fmt.Errorf("render the page template %s: %w\nTry: report this generator bug", name, err)
	}
	return b.Bytes(), nil
}

// pageData is the input of the page templates.
type pageData struct {
	// Pascal is the schema name, such as LineItem.
	Pascal string
	// Words and PluralWords name the resource in text, such as "line item".
	Words, PluralWords string
	// Name and Title are Words and PluralWords with an upper case first letter.
	Name, Title string
	// Route is the web path of the list page, such as /line-items.
	Route string
	// APIPath is the API path of the list, such as /api/line_items.
	APIPath string
	// Auth is true when the app has the auth module: every load and action
	// requires a signed-in user.
	Auth bool
	// Fields are the form fields, in argument order.
	Fields []pageField
	// TitleCol is the first string field, the title of an item. It is empty
	// when the resource has no string field.
	TitleCol string
	// TitleLabel is the list header of the title column.
	TitleLabel string
	// Toggle is the first non-null bool field. The list toggles it inline.
	Toggle *pageField
	// Columns are the list columns after the title.
	Columns []pageField
	// Enums are the enum fields. schema.ts has a label map for each.
	Enums []pageField
	// ListIcon is true when a list column shows a check icon.
	ListIcon bool
	// ListBadge is true when a list column shows a badge.
	ListBadge bool
	// The helpers and components that the fields use.
	Date, Time, JSON, Control, Input, Checkbox bool
}

func words(snake string) string { return strings.ReplaceAll(snake, "_", " ") }

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func newPageData(spec Spec) pageData {
	d := pageData{
		Pascal:      spec.Name.Pascal(),
		Words:       words(spec.Name.Snake()),
		PluralWords: words(spec.Name.PluralSnake()),
		Route:       "/" + spec.Name.PluralKebab(),
		APIPath:     "/api/" + spec.Name.PluralSnake(),
	}
	d.Name, d.Title = capitalize(d.Words), capitalize(d.PluralWords)
	d.TitleLabel = d.Name
	for _, f := range spec.Fields {
		p := d.field(f)
		d.Fields = append(d.Fields, p)
		if p.Labels != "" {
			d.Enums = append(d.Enums, p)
		}
		switch {
		case f.Type.Name == "string" && d.TitleCol == "":
			d.TitleCol, d.TitleLabel = p.Col, p.Label
		case f.Type.Name == "bool" && !f.Nullable && d.Toggle == nil:
			toggle := p
			d.Toggle = &toggle
		case p.Cell != "":
			d.Columns = append(d.Columns, p)
			d.ListIcon = d.ListIcon || strings.Contains(p.Cell, "<CheckIcon")
			d.ListBadge = d.ListBadge || strings.Contains(p.Cell, "<Badge")
		}
	}
	return d
}

// indent adds prefix to every line after the first.
func indent(s, prefix string) string { return strings.ReplaceAll(s, "\n", "\n"+prefix) }
