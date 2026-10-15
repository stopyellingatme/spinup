// Package component is the `spinup g component <name>...` generator. It runs
// the shadcn-svelte CLI at the registry version in the lock, which adds the
// components to web/src/lib/components/ui. The generator plans no files and
// records nothing in the lock: the files are tool output, and
// `spinup upgrade --ui` reviews them.
package component

import (
	"context"
	"fmt"
	"regexp"
	"slices"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
)

func init() {
	registry.Register(gen.Generator{
		Name:  "component",
		Short: "Add shadcn-svelte components to the web app (needs the network)",
		Usage: "<name>...",
		Run:   plan,
	})
}

// DefaultRegistry is the shadcn-svelte version when the lock has no
// shadcn_registry answer.
const DefaultRegistry = "1.7.0"

var (
	componentName = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	version       = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
)

// Command returns the shadcn-svelte command that adds names, run in web/.
func Command(registryVersion string, names []string) gen.Command {
	argv := []string{"pnpm", "dlx", "shadcn-svelte@" + registryVersion, "add"}
	argv = append(argv, names...)
	return gen.Command{Dir: "web", Argv: append(argv, "--yes", "--no-deps-install")}
}

func plan(_ context.Context, in gen.Input) (*gen.Output, error) {
	if len(in.Args) == 0 {
		return nil, fmt.Errorf("expected at least one component name\nTry: spinup g component dialog (the names are at https://www.shadcn-svelte.com/docs/components)")
	}
	if in.Lock == nil || !slices.Contains(in.Lock.Packs, "web") {
		return nil, fmt.Errorf("the app has no web pack, so it has no web/src/lib/components\nTry: add components in an app made without --api-only")
	}
	for _, name := range in.Args {
		if !componentName.MatchString(name) {
			return nil, fmt.Errorf("component name %q is invalid: use lower-case letters, digits, and hyphens\nTry: spinup g component dialog dropdown-menu", name)
		}
	}
	reg := DefaultRegistry
	if v, ok := in.Lock.Answers["shadcn_registry"]; ok {
		s, isString := v.(string)
		if !isString || !version.MatchString(s) {
			return nil, fmt.Errorf("the lock answer shadcn_registry is %v, not a version such as %s\nTry: correct shadcn_registry in spinup.lock", v, DefaultRegistry)
		}
		reg = s
	}
	return &gen.Output{
		Commands: []gen.Command{Command(reg, in.Args)},
		Next:     []string{"cd web && pnpm install", "cd web && pnpm check"},
	}, nil
}
