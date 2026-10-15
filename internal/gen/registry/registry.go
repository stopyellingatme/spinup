package registry

import (
	"fmt"
	"sort"
	"sync"

	"github.com/stopyellingatme/spinup/internal/gen"
)

var (
	mu         sync.RWMutex
	generators []gen.Generator
)

// Register adds g and panics for empty or duplicate names, aliases, or a nil Run.
func Register(g gen.Generator) {
	mu.Lock()
	defer mu.Unlock()
	if g.Run == nil {
		panic("generator Run must not be nil")
	}
	names := append([]string{g.Name}, g.Aliases...)
	seen := make(map[string]bool)
	for _, existing := range generators {
		seen[existing.Name] = true
		for _, alias := range existing.Aliases {
			seen[alias] = true
		}
	}
	for _, name := range names {
		if name == "" || seen[name] {
			panic(fmt.Sprintf("empty or duplicate generator name or alias %q", name))
		}
		seen[name] = true
	}
	generators = append(generators, clone(g))
}

// Lookup finds a generator by its name or alias.
func Lookup(name string) (gen.Generator, bool) {
	mu.RLock()
	defer mu.RUnlock()
	for _, g := range generators {
		if g.Name == name {
			return clone(g), true
		}
		for _, alias := range g.Aliases {
			if alias == name {
				return clone(g), true
			}
		}
	}
	return gen.Generator{}, false
}

// All returns the generators sorted by name.
func All() []gen.Generator {
	mu.RLock()
	defer mu.RUnlock()
	all := make([]gen.Generator, len(generators))
	for i, g := range generators {
		all[i] = clone(g)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all
}

func clone(g gen.Generator) gen.Generator {
	g.Aliases = append([]string(nil), g.Aliases...)
	return g
}
