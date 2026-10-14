package render

import (
	"bytes"
	"fmt"
	"go/format"
	"io/fs"
	"path"
	"strings"
	"text/template"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// Input contains every dependency of a deterministic render.
type Input struct {
	Packs           []*templates.Pack
	Answers         map[string]any
	Stack           *stackversions.Stack
	TemplateVersion string
}

// Render creates a plan in pack order and then file order without changing input.
// It validates answers, renders paths and .tmpl sources, formats Go, and normalizes
// text. Duplicate output paths are errors. No partial plan is returned on failure.
func Render(in Input) (vfs.Plan, error) {
	answers, err := ValidateAnswers(in.Packs, in.Answers)
	if err != nil {
		return vfs.Plan{}, err
	}
	answers["Stack"], answers["TemplateVersion"] = in.Stack, in.TemplateVersion
	funcs := functions(in.Stack)
	var plan vfs.Plan
	origins := map[string]string{}
	for _, p := range in.Packs {
		if p.FS == nil {
			return vfs.Plan{}, fmt.Errorf("render pack %q: file system is nil\nTry: load the pack with its sources", p.Name)
		}
		for _, spec := range p.Files {
			enabled, err := spec.Matches(answers)
			if err != nil {
				return vfs.Plan{}, fmt.Errorf("render %s/%s: %w", p.Name, spec.Source, err)
			}
			if !enabled {
				continue
			}
			file, err := renderFile(p, spec, answers, funcs)
			if err != nil {
				return vfs.Plan{}, err
			}
			if first, ok := origins[file.Path]; ok {
				return vfs.Plan{}, fmt.Errorf("render duplicate output %q: templates %s and %s\nTry: give each template a unique output path", file.Path, first, file.Template)
			}
			origins[file.Path] = file.Template
			if err := plan.Add(file); err != nil {
				return vfs.Plan{}, fmt.Errorf("stage output %q from %s: %w\nTry: use a unique clean output path", file.Path, file.Template, err)
			}
		}
	}
	return plan, nil
}

func renderFile(p *templates.Pack, spec templates.FileSpec, data map[string]any, funcs template.FuncMap) (vfs.File, error) {
	origin := p.Name + "/" + spec.Source
	filename, err := execute(origin+" output path", spec.Path, data, funcs)
	if err != nil {
		return vfs.File{}, err
	}
	clean, err := vfs.CleanPath(string(filename))
	if err != nil {
		return vfs.File{}, fmt.Errorf("render path from %s: %w\nTry: use a clean relative output path", origin, err)
	}
	if clean != string(filename) || strings.ContainsRune(clean, 0) {
		return vfs.File{}, fmt.Errorf("render path %q from %s: path is not clean\nTry: use a clean relative output path", filename, origin)
	}
	if !manifest.Class(spec.Class).Valid() {
		return vfs.File{}, fmt.Errorf("render %q from %s: invalid class %q\nTry: use managed, scaffold, or seed", clean, origin, spec.Class)
	}
	if !fs.ValidPath(spec.Source) || strings.ContainsAny(spec.Source, "\\\x00") {
		return vfs.File{}, fmt.Errorf("read template %s: source path is not clean\nTry: use a clean relative source path", origin)
	}
	mode, err := spec.Permissions()
	if err != nil {
		return vfs.File{}, err
	}
	content, err := fs.ReadFile(p.FS, spec.Source)
	if err != nil {
		return vfs.File{}, fmt.Errorf("read template %s for %q: %w\nTry: provide a readable source file", origin, clean, err)
	}
	if strings.HasSuffix(spec.Source, ".tmpl") {
		content, err = execute(origin, string(content), data, funcs)
		if err != nil {
			return vfs.File{}, fmt.Errorf("render output %q: %w", clean, err)
		}
	}
	if path.Ext(clean) == ".go" {
		content, err = format.Source(content)
		if err != nil {
			return vfs.File{}, fmt.Errorf("format output %q from template %s: %w\nTry: correct the Go template or its answers", clean, origin, err)
		}
	}
	return vfs.File{Path: clean, Data: manifest.Normalize(content), Mode: mode, Class: spec.Class, Template: origin}, nil
}

func execute(name, source string, data map[string]any, funcs template.FuncMap) ([]byte, error) {
	tmpl, err := template.New(name).Delims("[[", "]]").Option("missingkey=error").Funcs(funcs).Parse(source)
	if err != nil {
		return nil, fmt.Errorf("parse template %s: %w\nTry: correct the template syntax", name, err)
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, data); err != nil {
		return nil, fmt.Errorf("execute template %s: %w\nTry: correct the template answers or stack lookup", name, err)
	}
	return output.Bytes(), nil
}
