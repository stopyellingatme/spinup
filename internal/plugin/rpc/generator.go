package rpc

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"slices"
	"strings"

	execx "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/plugin/pack"
	pluginrender "github.com/stopyellingatme/spinup/internal/plugin/render"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/vfs"
	"github.com/stopyellingatme/spinup/plugin-sdk/sdk"
)

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Options configures project plugin discovery and process execution.
type Options struct {
	Root    string
	Offline bool
	Runner  execx.Runner
	Stderr  io.Writer
}

// ProjectGenerators adds declarative and executable generators to builtins.
// Outside a project it returns only the supplied generators.
func ProjectGenerators(ctx context.Context, dir string, offline bool, builtins []gen.Generator, stderr io.Writer) ([]gen.Generator, error) {
	root, _ := project.FindRoot(dir)
	if root == "" {
		return slices.Clone(builtins), nil
	}
	cfg, err := project.Load(root)
	if err != nil {
		return nil, fmt.Errorf("load plugin generators: %w\nTry: repair spinup.toml", err)
	}
	if len(cfg.Plugins) == 0 {
		return slices.Clone(builtins), nil
	}
	plugins, err := plugin.Resolve(ctx, plugin.Options{Root: root, Offline: offline})
	if err != nil {
		return nil, err
	}
	all, err := pluginrender.Generators(plugins, builtins)
	if err != nil {
		return nil, err
	}
	return Generators(ctx, plugins, all, Options{Root: root, Offline: offline, Stderr: stderr})
}

// Generators adapts generators from trusted executable plugins and rejects name collisions.
func Generators(ctx context.Context, plugins []plugin.Loaded, builtins []gen.Generator, o Options) ([]gen.Generator, error) {
	all := slices.Clone(builtins)
	seen := map[string]bool{}
	for _, g := range all {
		seen[g.Name] = true
		for _, alias := range g.Aliases {
			seen[alias] = true
		}
	}
	for _, p := range plugins {
		config, err := pack.ReadExec(p.FS)
		if err != nil {
			return nil, err
		}
		if config.Kind != "exec" {
			continue
		}
		client := &Client{Plugin: p, Root: o.Root, Runner: o.Runner, Stderr: o.Stderr}
		description, err := client.Describe(ctx)
		if err != nil {
			return nil, err
		}
		for _, spec := range description.Generators {
			if !identifier.MatchString(spec.Name) || seen[spec.Name] {
				return nil, fmt.Errorf("load exec generator %q: invalid or duplicate name\nTry: rename the plugin generator", spec.Name)
			}
			seen[spec.Name] = true
			all = append(all, gen.Generator{Name: spec.Name, Short: spec.Short, Usage: strings.TrimPrefix(spec.Usage, spec.Name+" "), Run: func(ctx context.Context, in gen.Input) (*gen.Output, error) {
				if len(in.Args) != len(spec.Args) {
					return nil, fmt.Errorf("generate %s: expected %d arguments, got %d\nTry: spinup generate %s --help", spec.Name, len(spec.Args), len(in.Args), spec.Name)
				}
				var answers map[string]any
				if in.Lock != nil {
					answers = in.Lock.Answers
				}
				invocation := *client
				invocation.Root = in.Root
				result, err := invocation.Plan(ctx, sdk.PlanParams{Generator: spec.Name, Args: in.Args, Answers: answers})
				if err != nil {
					return nil, err
				}
				return generatorOutput(p.Meta.Name, spec.Name, result)
			}})
		}
	}
	slices.SortFunc(all, func(a, b gen.Generator) int { return strings.Compare(a.Name, b.Name) })
	return all, nil
}

func generatorOutput(pluginName, generator string, result sdk.PlanResult) (*gen.Output, error) {
	output := &gen.Output{Next: result.Next}
	if len(result.Next) > 2 {
		return nil, fmt.Errorf("plan exec generator %s: more than two next commands\nTry: correct the plugin plan", generator)
	}
	for _, file := range result.Files {
		if err := pack.CheckOutputPath(file.Path); err != nil {
			return nil, err
		}
		if !manifest.Class(file.Class).Valid() {
			return nil, fmt.Errorf("plan exec generator %s: invalid class %q\nTry: return managed, scaffold, or seed files", generator, file.Class)
		}
		if file.Mode & ^uint32(0777) != 0 {
			return nil, fmt.Errorf("plan exec generator %s: invalid mode %o\nTry: return only file permission bits", generator, file.Mode)
		}
		mode := fs.FileMode(file.Mode)
		if mode == 0 {
			mode = 0644
		}
		if err := output.Plan.Add(vfs.File{Path: file.Path, Data: manifest.Normalize(file.Data), Class: file.Class, Mode: mode, Template: "plugin/" + pluginName + "/@exec/" + generator}); err != nil {
			return nil, err
		}
	}
	output.Plan = output.Plan.Sorted()
	return output, nil
}
