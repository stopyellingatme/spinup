package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/compat"
)

func phaseDeps(ctx context.Context, s *Step) error {
	hasAPI, err := phaseHasAPI(s)
	if err != nil || !hasAPI {
		return err
	}
	result, err := phaseGo(ctx, s, "mod", "edit", "-json")
	if err != nil {
		return err
	}
	var mod struct {
		Require []struct {
			Path, Version string
			Indirect      bool
		}
	}
	if err := json.Unmarshal(result.Stdout, &mod); err != nil {
		return fmt.Errorf("decode go mod edit -json: %w\nTry: repair api/go.mod in the upgrade worktree", err)
	}
	requires := make(map[string]string)
	for _, requirement := range mod.Require {
		if !requirement.Indirect {
			requires[requirement.Path] = requirement.Version
		}
	}
	var names []string
	if s.To.Stack != nil {
		for name := range s.To.Stack.GoModules {
			if name != kitModule && requires[name] != "" {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	for _, name := range names {
		pin := s.To.Stack.GoModules[name]
		current, err := dependencyVersion(requires[name])
		if err != nil {
			return fmt.Errorf("compare dependency %s: %w\nTry: repair its version in api/go.mod", name, err)
		}
		target, err := dependencyVersion(pin)
		if err != nil {
			return fmt.Errorf("compare dependency pin %s: %w\nTry: repair the target stack versions", name, err)
		}
		if current.Compare(target) < 0 {
			if err := requirePin(ctx, s, name, requires[name], pin); err != nil {
				return err
			}
		}
	}
	version, err := compat.ParseVersion(s.To.Version)
	if err != nil {
		return fmt.Errorf("select kit version: %w\nTry: choose a valid target template version", err)
	}
	// Pin only: raise the kit to the step's version, never lower it (resolution 8).
	raiseKit := version.Pre == ""
	if raiseKit && requires[kitModule] != "" {
		current, err := dependencyVersion(requires[kitModule])
		raiseKit = err != nil || current.Compare(version) < 0
	}
	if raiseKit {
		if err := requirePin(ctx, s, kitModule, requires[kitModule], version.String()); err != nil {
			return err
		}
		s.Lock.Kit.Module = kitModule
		s.Lock.Kit.Version = version.String()
	}
	if _, err := phaseGo(ctx, s, "mod", "tidy"); err != nil {
		return fmt.Errorf("update dependencies with go mod tidy: %w\nTry: rerun with network access, or pass --no-verify and fix go.mod by hand", err)
	}
	return nil
}

func dependencyVersion(version string) (compat.Version, error) {
	// Go module versions may have +incompatible or other build metadata.
	core, _, _ := strings.Cut(version, "+")
	return compat.ParseVersion(core)
}

func requirePin(ctx context.Context, s *Step, module, from, to string) error {
	if _, err := phaseGo(ctx, s, "mod", "edit", "-require="+module+"@"+to); err != nil {
		return err
	}
	return phasePrint(s, "Dependency %s: %s -> %s\n", module, from, to)
}
