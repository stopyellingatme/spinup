package modules

import (
	"context"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/gen/newapp"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/render"
	"github.com/stopyellingatme/spinup/internal/templates"
)

// Selection contains the target packs and answers for a module addition.
type Selection struct {
	// Packs lists all target packs in dependency order.
	Packs []string
	// Answers contains the completed target answers.
	Answers map[string]any
	// Added lists newly selected modules in name order.
	Added []string
	// Required lists automatically selected module dependencies.
	Required []string
}

// SelectOptions supplies the project, locked packs, requested modules, and question policy.
type SelectOptions struct {
	// Config supplies the current module settings.
	Config *project.Config
	// Lock supplies the previous pack selection and answers.
	Lock *manifest.Lock
	// FS contains the packs of the locked template version.
	FS fs.FS
	// Names lists the requested modules.
	Names []string
	// Answers supplies explicit question values.
	Answers map[string]any
	// Interactive permits questions on a terminal.
	Interactive bool
	// Prompter obtains answers when Interactive is true.
	Prompter newapp.Prompter
}

// Select validates module additions and completes the new packs' questions without writing files.
func Select(ctx context.Context, o SelectOptions) (*Selection, error) {
	if o.Config == nil || o.Lock == nil || o.FS == nil {
		return nil, fmt.Errorf("select modules: missing project or locked templates\nTry: restore spinup.toml and spinup.lock")
	}
	if len(o.Names) == 0 {
		return nil, fmt.Errorf("select modules: no module names\nTry: spinup module add <name>")
	}
	requested := map[string]bool{}
	for _, name := range o.Names {
		if _, ok := Get(name); !ok {
			return nil, fmt.Errorf("add module %q: unknown module\nTry: spinup module list", name)
		}
		requested[name] = true
	}
	selected := map[string]*templates.Pack{}
	visiting := map[string]bool{}
	var add func(string) error
	add = func(name string) error {
		if selected[name] != nil {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("select module %q: dependency cycle\nTry: use packs without cyclic requirements", name)
		}
		if name == "web" && !WebEnabled(o.Lock) {
			return fmt.Errorf("add modules: a requested module requires web, but this app is API-only\nTry: select modules without a web requirement")
		}
		visiting[name] = true
		p, err := templates.Load(o.FS, name)
		if err != nil {
			return err
		}
		for _, dep := range p.Requires {
			if err := add(dep); err != nil {
				return err
			}
		}
		delete(visiting, name)
		selected[name] = p
		return nil
	}
	// Preserve all locked packs, including non-module packs supplied by plugins.
	for _, name := range o.Lock.Packs {
		if err := add(name); err != nil {
			return nil, err
		}
	}
	for _, name := range append(slices.Clone(o.Config.Modules), o.Names...) {
		if err := add(name); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	slices.Sort(names)
	result := &Selection{Added: []string{}, Required: []string{}}
	done := map[string]bool{}
	var ordered []*templates.Pack
	var fresh []*templates.Pack
	var enabled []string
	for len(ordered) < len(names) {
		for _, name := range names {
			if done[name] {
				continue
			}
			p := selected[name]
			if !allDone(p.Requires, done) {
				continue
			}
			done[name] = true
			ordered = append(ordered, p)
			result.Packs = append(result.Packs, name)
			if !core(name) {
				if _, ok := Get(name); ok {
					enabled = append(enabled, name)
				}
			}
			if !slices.Contains(o.Lock.Packs, name) {
				fresh = append(fresh, p)
				if !core(name) {
					result.Added = append(result.Added, name)
					if !requested[name] {
						result.Required = append(result.Required, name)
					}
				}
			}
			break
		}
	}
	slices.Sort(enabled)
	slices.Sort(result.Added)
	slices.Sort(result.Required)
	answers := make(map[string]any, len(o.Lock.Answers))
	for key, value := range o.Lock.Answers {
		answers[key] = value
	}
	answers["modules"] = enabled
	questions, err := newapp.Questions(fresh)
	if err != nil {
		return nil, err
	}
	for _, q := range questions {
		if value, explicit := o.Answers[q.Key]; explicit {
			answers[q.Key] = value
			continue
		}
		// A fresh monitoring pack inherits the user's configuration preference.
		// Replace stale lock answers too, but let an explicit flag take precedence.
		if q.Key == "monitoring_profile" && slices.Contains(q.Choices, o.Config.Monitoring.Profile) {
			q.Default = o.Config.Monitoring.Profile
			delete(answers, q.Key)
		}
		if _, exists := answers[q.Key]; exists {
			continue
		}
		var value any
		if q.HasDefault() {
			value = q.Default
			if q.Type == "bool" {
				value = q.Default == "true"
			}
		}
		if o.Interactive {
			if o.Prompter == nil {
				return nil, fmt.Errorf("ask module question %q: no prompter\nTry: pass --%s", q.Key, strings.ReplaceAll(q.Key, "_", "-"))
			}
			value, err = o.Prompter.Ask(ctx, q, value)
			if err != nil {
				return nil, err
			}
		}
		if value != nil {
			answers[q.Key] = value
		}
	}
	// Reject flags that cannot affect the new packs instead of silently ignoring them.
	for key := range o.Answers {
		if !slices.ContainsFunc(questions, func(q templates.Question) bool { return q.Key == key }) {
			return nil, fmt.Errorf("answer module question %q: it does not belong to a new pack\nTry: pass flags only for modules being added", key)
		}
	}
	result.Answers, err = render.ValidateAnswers(ordered, answers)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func allDone(names []string, done map[string]bool) bool {
	for _, name := range names {
		if !done[name] {
			return false
		}
	}
	return true
}
