package upgrade

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/stopyellingatme/spinup/internal/render"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// Render renders the selected packs in their given order for a template version.
func Render(v *templates.Version, packs []string, answers map[string]any) (vfs.Plan, error) {
	loaded, completed, err := loadPlanPacks(v, packs, answers)
	if err != nil {
		return vfs.Plan{}, err
	}
	return render.Render(render.Input{Packs: loaded, Answers: completed, Stack: v.Stack, TemplateVersion: v.Version})
}

func loadPlanPacks(v *templates.Version, names []string, answers map[string]any) ([]*templates.Pack, map[string]any, error) {
	if v == nil {
		return nil, nil, fmt.Errorf("load upgrade templates: version is nil\nTry: load the target template version")
	}
	packs := make([]*templates.Pack, 0, len(names))
	for _, name := range names {
		pack, err := templates.Load(v.Packs, name)
		if err != nil {
			return nil, nil, fmt.Errorf("load pack %q from version %q: %w\nTry: select a version containing a valid pack %q", name, v.Version, err, name)
		}
		packs = append(packs, pack)
	}
	completed, err := render.ValidateAnswers(packs, answers)
	return packs, completed, err
}

// RenamedFrom returns rendered destination paths mapped to their previous paths.
func RenamedFrom(v *templates.Version, packs []string, answers map[string]any) (map[string]string, error) {
	loaded, completed, err := loadPlanPacks(v, packs, answers)
	if err != nil {
		return nil, err
	}
	completed["Stack"], completed["TemplateVersion"] = v.Stack, v.Version
	result := make(map[string]string)
	for _, pack := range loaded {
		for _, spec := range pack.Files {
			if spec.RenamedFrom == "" {
				continue
			}
			enabled, err := spec.Matches(completed)
			if err != nil {
				return nil, err
			}
			if !enabled {
				continue
			}
			target, err := renderRenamePath(spec.Path, completed)
			if err != nil {
				return nil, err
			}
			source, err := renderRenamePath(spec.RenamedFrom, completed)
			if err != nil {
				return nil, err
			}
			if _, exists := result[target]; exists {
				return nil, fmt.Errorf("render rename %q: duplicate destination\nTry: give each rename a unique destination", target)
			}
			result[target] = source
		}
	}
	return result, nil
}

func renderRenamePath(source string, answers map[string]any) (string, error) {
	tmpl, err := template.New("rename path").Delims("[[", "]]").Option("missingkey=error").Parse(source)
	if err != nil {
		return "", fmt.Errorf("parse rename path %q: %w\nTry: correct the rename template", source, err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, answers); err != nil {
		return "", fmt.Errorf("render rename path %q: %w\nTry: supply the required answers", source, err)
	}
	name := out.String()
	clean, err := vfs.CleanPath(name)
	if err != nil {
		return "", err
	}
	if clean != name {
		return "", fmt.Errorf("render rename path %q: path is not clean\nTry: use a clean relative path", name)
	}
	return name, nil
}
