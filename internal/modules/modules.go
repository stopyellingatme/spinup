package modules

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/templates"
)

// Module describes a built-in optional pack.
type Module struct {
	// Name identifies the pack.
	Name string `json:"name"`
	// Description explains the module's purpose.
	Description string `json:"description"`
	// Requires lists the pack dependencies.
	Requires []string `json:"requires"`
	// NeedsWeb reports a direct or transitive web dependency.
	NeedsWeb bool `json:"needs_web"`
}

var catalog = sync.OnceValue(func() []Module {
	fsys := templates.Embedded()
	names, err := templates.List(fsys)
	if err != nil {
		panic(err)
	}
	packs := map[string]*templates.Pack{}
	for _, name := range names {
		if strings.HasPrefix(name, "_") {
			continue
		}
		p, err := templates.Load(fsys, name)
		if err != nil {
			panic(err)
		}
		packs[name] = p
	}
	var needsWeb func(string, map[string]bool) bool
	needsWeb = func(name string, visiting map[string]bool) bool {
		if name == "web" {
			return true
		}
		if visiting[name] || packs[name] == nil {
			panic("invalid embedded pack dependency: " + name)
		}
		visiting[name] = true
		defer delete(visiting, name)
		for _, dep := range packs[name].Requires {
			if needsWeb(dep, visiting) {
				return true
			}
		}
		return false
	}
	result := []Module{}
	for _, name := range names {
		if core(name) || packs[name] == nil {
			continue
		}
		p := packs[name]
		result = append(result, Module{Name: name, Description: p.Description, Requires: p.Requires, NeedsWeb: needsWeb(name, map[string]bool{})})
	}
	return result
})

func core(name string) bool { return name == "base" || name == "api" || name == "web" }

func clone(m Module) Module { m.Requires = append([]string{}, m.Requires...); return m }

// All returns independent module metadata in name order.
func All() []Module {
	result := make([]Module, 0, len(catalog()))
	for _, m := range catalog() {
		result = append(result, clone(m))
	}
	return result
}

// Get returns the named module and whether it exists. Core packs are not modules.
func Get(name string) (Module, bool) {
	for _, m := range catalog() {
		if m.Name == name {
			return clone(m), true
		}
	}
	return Module{}, false
}

// Enabled reports whether cfg enables a known module. An unknown name is a programming error.
func Enabled(cfg *project.Config, name string) bool {
	if _, ok := Get(name); !ok {
		panic(fmt.Sprintf("unknown built-in module %q", name))
	}
	return cfg != nil && slices.Contains(cfg.Modules, name)
}

// WebEnabled reports whether the lock selects the web pack, which is not a module.
func WebEnabled(lock *manifest.Lock) bool { return lock != nil && slices.Contains(lock.Packs, "web") }
