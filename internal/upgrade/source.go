package upgrade

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/plugin/pack"
	pluginrender "github.com/stopyellingatme/spinup/internal/plugin/render"
	"github.com/stopyellingatme/spinup/internal/project"
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
	plan, err := render.Render(render.Input{Packs: loaded, Answers: completed, Stack: v.Stack, TemplateVersion: v.Version})
	if err != nil {
		return vfs.Plan{}, err
	}
	return pluginrender.Prefix(v.Packs, plan), nil
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

// LockedPacks builds the core and plugin selection for the project's rendered lock.
// Historical plugins use approved history and cached bytes, including removed plugins.
func LockedPacks(ctx context.Context, root string, lock *manifest.Lock, core *templates.Version) (PackSet, error) {
	historical, err := pack.ResolveLocked(ctx, plugin.Options{Root: root, Offline: true}, lock.Plugins)
	if err != nil {
		return PackSet{}, err
	}
	if core == nil {
		if len(historical) == 0 {
			return PackSet{}, nil
		}
		return PackSet{}, fmt.Errorf("load locked plugin packs: template version is unavailable\nTry: restore the old template version before upgrading")
	}
	v, names, err := pluginrender.Overlay(core, lock.Packs, historical)
	if err != nil {
		return PackSet{}, err
	}
	return PackSet{FS: v.Packs, Names: names}, nil
}

// pluginPacks loads OLD from the rendered lock and NEW from current approvals.
// It uses the project's source paths, even when planning in a temporary directory.
func (r *Runner) pluginPacks(ctx context.Context, lock *manifest.Lock, old, newer *templates.Version, target PackSet, offline bool) (PackSet, PackSet, error) {
	loaded, err := r.declaredPlugins(ctx, lock, offline)
	if err != nil {
		return PackSet{}, PackSet{}, err
	}
	oldSet, err := LockedPacks(ctx, r.Root, lock, old)
	if err != nil {
		return PackSet{}, PackSet{}, err
	}
	_, names := target.resolve(newer, lock.Packs)
	names = slices.Clone(names)
	// Rebuild plugin selections from NEW, preserving the order of core packs.
	names = slices.DeleteFunc(names, func(name string) bool {
		for _, p := range lock.Plugins {
			if p.Name == name {
				return true
			}
		}
		for _, p := range loaded {
			if p.Meta.Name == name {
				return true
			}
		}
		return false
	})
	v, names, err := pluginrender.Overlay(newer, names, loaded)
	if err != nil {
		return PackSet{}, PackSet{}, err
	}
	return oldSet, PackSet{FS: v.Packs, Names: names}, nil
}

func (r *Runner) pluginChain(ctx context.Context, lock *manifest.Lock, versions []string, offline bool) ([]string, error) {
	if len(versions) > 0 {
		return versions, nil
	}
	loaded, err := r.declaredPlugins(ctx, lock, offline)
	if err != nil {
		return nil, err
	}
	if len(lock.Plugins) != len(loaded) {
		return []string{lock.TemplateVersion}, nil
	}
	locked := make(map[string]manifest.Plugin, len(lock.Plugins))
	for _, p := range lock.Plugins {
		locked[p.Name] = p
	}
	for _, p := range loaded {
		entry, exists := locked[p.Meta.Name]
		if !exists || strings.TrimPrefix(entry.Version, "v") != strings.TrimPrefix(p.Meta.Version, "v") || entry.SHA256 != p.SHA256 || entry.API != p.Meta.API {
			return []string{lock.TemplateVersion}, nil
		}
	}
	return versions, nil
}

func (r *Runner) declaredPlugins(ctx context.Context, lock *manifest.Lock, offline bool) ([]plugin.Loaded, error) {
	if _, err := os.Stat(filepath.Join(r.Root, project.ConfigFile)); os.IsNotExist(err) && len(lock.Plugins) == 0 {
		return nil, nil
	}
	return plugin.Resolve(ctx, plugin.Options{Root: r.Root, Offline: offline, Out: r.Out})
}
