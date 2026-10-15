package upgrade

import (
	"fmt"
	"maps"
	"runtime"
	"slices"

	"github.com/stopyellingatme/spinup/internal/manifest"
	pluginrender "github.com/stopyellingatme/spinup/internal/plugin/render"
	"github.com/stopyellingatme/spinup/internal/templates"
)

// NextLock records NEW template content, preserving user edits for later upgrades.
// It returns an independent lock and never modifies the old lock or the plan.
// The optional target selects NEW packs and their filesystem. Omission keeps old.Packs.
func NextLock(old *manifest.Lock, p *Plan, to *templates.Version, answers map[string]any, target ...PackSet) (*manifest.Lock, error) {
	if err := old.Validate(); err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("build next lock: plan is nil\nTry: build an upgrade plan first")
	}
	if len(target) > 1 {
		return nil, fmt.Errorf("build next lock: multiple target pack sets\nTry: supply one target pack set")
	}
	selection := PackSet{}
	if len(target) == 1 {
		selection = target[0]
	}
	version, packs := selection.resolve(to, old.Packs)
	_, completed, err := loadPlanPacks(version, packs, answers)
	if err != nil {
		return nil, err
	}
	rendered, err := Render(version, packs, completed)
	if err != nil {
		return nil, err
	}
	next := *old
	next.TemplateVersion = to.Version
	next.FormatterGo = runtime.Version()
	next.Answers = clonePlanAnswers(completed)
	next.Packs = slices.Clone(packs)
	next.Plugins = pluginrender.Plugins(version.Packs)
	next.AppliedMigrations = slices.Clone(old.AppliedMigrations)
	next.Tombstones = slices.Clone(old.Tombstones)
	next.Orphans = slices.Clone(old.Orphans)
	next.Files = maps.Clone(old.Files)
	if next.Files == nil {
		next.Files = make(map[string]manifest.FileEntry)
	}
	next.StackVersions = make(map[string]string)
	if to.Stack != nil {
		keys := make([]string, 0, len(to.Stack.Images))
		for key := range to.Stack.Images {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			next.StackVersions[key] = to.Stack.Images[key].Ref()
		}
	}
	for _, name := range p.Tombstoned {
		next.Tombstone(name)
	}
	for _, d := range p.Files {
		if d.From != "" {
			delete(next.Files, d.From)
		}
		switch d.Action {
		case ActionOrphan:
			delete(next.Files, d.Path)
			next.Orphans = append(next.Orphans, d.Path)
		case ActionDelete:
			delete(next.Files, d.Path)
		default:
			// Tombstones and generator-owned files keep their old lock entry.
			if slices.Contains(next.Tombstones, d.Path) || d.Reason == "tombstone" || d.Reason == "generator-owned" {
				continue
			}
			file, ok := rendered.Get(d.Path)
			if !ok {
				continue
			}
			previous := old.Files[d.Path]
			if d.From != "" {
				previous = old.Files[d.From]
			}
			entry := manifest.FileEntry{Class: planClass(previous.Class, manifest.Class(file.Class))}
			if entry.Class != manifest.Seed {
				entry.Template = file.Template
				entry.Hash = manifest.Hash(file.Data)
				entry.Mode = fmt.Sprintf("%04o", file.Mode.Perm())
			}
			next.Files[d.Path] = entry
			next.Orphans = slices.DeleteFunc(next.Orphans, func(name string) bool { return name == d.Path })
		}
	}
	slices.Sort(next.Tombstones)
	next.Tombstones = slices.Compact(next.Tombstones)
	slices.Sort(next.Orphans)
	next.Orphans = slices.Compact(next.Orphans)
	if err := next.Validate(); err != nil {
		return nil, err
	}
	return &next, nil
}

func clonePlanAnswers(answers map[string]any) map[string]any {
	result := make(map[string]any, len(answers))
	for key, value := range answers {
		result[key] = clonePlanAnswer(value)
	}
	return result
}

func clonePlanAnswer(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return clonePlanAnswers(v)
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = clonePlanAnswer(item)
		}
		return result
	case []string:
		return slices.Clone(v)
	default:
		return value
	}
}
