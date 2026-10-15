package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/stopyellingatme/spinup/internal/compat"
	"github.com/stopyellingatme/spinup/internal/webtool"
)

func phaseDeps(ctx context.Context, s *Step) error {
	if err := goDeps(ctx, s); err != nil {
		return err
	}
	return webDeps(ctx, s)
}

// webDeps refreshes the package manager lockfile from the upgraded web/package.json. The lockfile is a
// seed file that upgrades never merge, so the dependency step regenerates it (ADR 0008).
func webDeps(ctx context.Context, s *Step) error {
	_, err := os.Stat(filepath.Join(s.Worktree, "web", "package.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect web/package.json: %w\nTry: check the upgrade worktree permissions", err)
	}
	tool := webtool.FromLock(s.Lock)
	if s.Answers != nil {
		tool = webtool.For(s.Answers)
	}
	lockfile := "web/" + tool.Lockfile()
	if s.Plan != nil && slices.Contains(s.Plan.Conflicts(), "web/package.json") {
		_, _ = fmt.Fprintf(s.Out, "Skipped %s: web/package.json has conflicts. Resolve them, then run %s install in web/.\n", lockfile, tool.Name())
		return nil
	}
	command := tool.RefreshLock()
	command.Dir = filepath.Join(s.Worktree, "web")
	result, err := s.Runner.Run(ctx, command)
	if err == nil && result.ExitCode != 0 {
		err = fmt.Errorf("exit code %d", result.ExitCode)
	}
	if err != nil {
		return fmt.Errorf("update %s with %s: %w\n%s\nTry: rerun with network access, or run %s install in %s and then spinup upgrade --continue", lockfile, command.String(), err, strings.TrimSpace(string(result.Stderr)), tool.Name(), command.Dir)
	}
	return nil
}

func goDeps(ctx context.Context, s *Step) error {
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
