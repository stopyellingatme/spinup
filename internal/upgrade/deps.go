package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	if err := raiseKitRequire(ctx, s, requires[kitModule]); err != nil {
		return err
	}
	if err := raiseGoDirective(ctx, s); err != nil {
		return err
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

// raiseKitRequire raises the kit to the higher of the template version (a release only) and the
// kit pin of the target stack. It never lowers the kit (resolution 8).
func raiseKitRequire(ctx context.Context, s *Step, current string) error {
	version, err := compat.ParseVersion(s.To.Version)
	if err != nil {
		return fmt.Errorf("select kit version: %w\nTry: choose a valid target template version", err)
	}
	var target *compat.Version
	if version.Pre == "" {
		target = &version
	}
	if s.To.Stack != nil && s.To.Stack.GoModules[kitModule] != "" {
		pin, err := dependencyVersion(s.To.Stack.GoModules[kitModule])
		if err != nil {
			return fmt.Errorf("compare dependency pin %s: %w\nTry: repair the target stack versions", kitModule, err)
		}
		if target == nil || target.Compare(pin) < 0 {
			target = &pin
		}
	}
	if target == nil {
		return nil
	}
	if current != "" && !placeholderKit(current, *target) {
		have, err := dependencyVersion(current)
		if err == nil && have.Compare(*target) >= 0 {
			return nil
		}
	}
	if err := requirePin(ctx, s, kitModule, current, target.String()); err != nil {
		return err
	}
	s.Lock.Kit.Module = kitModule
	s.Lock.Kit.Version = target.String()
	return nil
}

var goDirective = regexp.MustCompile(`(?m)^go[ \t]+(\d+\.\d+(?:\.\d+)?)[ \t]*$`)

// raiseGoDirective raises the go line of api/go.mod to the go line of the new render. The render is
// the Data of the kept api/go.mod decision. It never lowers the line.
func raiseGoDirective(ctx context.Context, s *Step) error {
	if s.Plan == nil {
		return nil
	}
	var render []byte
	for _, file := range s.Plan.Files {
		if file.Path == "api/go.mod" {
			render = file.Data
		}
	}
	want := goDirective.FindSubmatch(render)
	if want == nil {
		return nil
	}
	disk, err := os.ReadFile(filepath.Join(s.Worktree, "api", "go.mod"))
	if err != nil {
		return fmt.Errorf("read api/go.mod: %w\nTry: check the upgrade worktree permissions", err)
	}
	have := goDirective.FindSubmatch(disk)
	target, err := goLanguageVersion(string(want[1]))
	if err != nil {
		return fmt.Errorf("read the go line of the new api/go.mod: %w\nTry: repair the go line in the template", err)
	}
	if have != nil {
		current, err := goLanguageVersion(string(have[1]))
		if err == nil && current.Compare(target) >= 0 {
			return nil
		}
	}
	if _, err := phaseGo(ctx, s, "mod", "edit", "-go="+string(want[1])); err != nil {
		return err
	}
	return phasePrint(s, "Go version: %s -> %s\n", orNone(have), want[1])
}

func orNone(match [][]byte) string {
	if match == nil {
		return "none"
	}
	return string(match[1])
}

// goLanguageVersion parses a go line such as 1.26 or 1.26.0.
func goLanguageVersion(v string) (compat.Version, error) {
	if strings.Count(v, ".") == 1 {
		v += ".0"
	}
	return compat.ParseVersion(v)
}

// placeholderKit reports whether current is the kit version v0.1.0 that templates required
// before the kit was published (ADR 0018). Nobody published v0.1.0, so an app that requires it
// builds only with a local replace. A pre-release of v0.1.0 is lower in semver, so the
// never-lower rule would keep the placeholder. The pin replaces it.
func placeholderKit(current string, target compat.Version) bool {
	return current == "v0.1.0" && target.Pre != "" && target.Major == 0 && target.Minor == 1 && target.Patch == 0
}
