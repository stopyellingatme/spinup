package render

import (
	"bytes"
	"context"
	"fmt"
	"go/format"
	"io/fs"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/spf13/pflag"
	"github.com/stopyellingatme/spinup/internal/gen"
	genrun "github.com/stopyellingatme/spinup/internal/gen/run"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/plugin/pack"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// Generators adapts declarative plugin generators and rejects duplicate names or aliases.
// The caller supplies built-in generators so plugin declarations cannot shadow them.
func Generators(plugins []plugin.Loaded, builtins []gen.Generator) ([]gen.Generator, error) {
	result := slices.Clone(builtins)
	seen := map[string]bool{}
	for _, g := range builtins {
		seen[g.Name] = true
		for _, alias := range g.Aliases {
			seen[alias] = true
		}
	}
	for _, p := range plugins {
		for _, spec := range p.Meta.Generators {
			if spec.Name == "" || seen[spec.Name] {
				return nil, fmt.Errorf("load plugin generator %q: name collides with another generator\nTry: rename the plugin generator", spec.Name)
			}
			seen[spec.Name] = true
			result = append(result, gen.Generator{Name: spec.Name, Short: spec.Short, Usage: strings.TrimPrefix(spec.Usage, spec.Name+" "), Run: func(ctx context.Context, in gen.Input) (*gen.Output, error) { return generate(ctx, p, spec, in) }})
		}
	}
	slices.SortFunc(result, func(a, b gen.Generator) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}

// ProjectGenerators resolves trusted plugins for the selected project.
// Outside a project it returns the built-in generators.
func ProjectGenerators(ctx context.Context, dir string, offline bool, builtins []gen.Generator) ([]gen.Generator, error) {
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
	return Generators(plugins, builtins)
}

func generate(ctx context.Context, p plugin.Loaded, spec plugin.GeneratorSpec, in gen.Input) (*gen.Output, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("generate plugin files: %w\nTry: retry the generator", err)
	}
	if len(in.Args) != len(spec.Args) {
		return nil, fmt.Errorf("generate %s: expected %d arguments, got %d\nTry: spinup generate %s --help", spec.Name, len(spec.Args), len(in.Args), spec.Name)
	}
	data := maps.Clone(in.Lock.Answers)
	if data == nil {
		data = map[string]any{}
	}
	for i, key := range spec.Args {
		data[key] = in.Args[i]
	}
	output := &gen.Output{}
	for _, file := range spec.Files {
		dest, err := generatorPath(file.Path, data)
		if err != nil {
			return nil, err
		}
		if err := pack.CheckOutputPath(dest); err != nil {
			return nil, err
		}
		source, err := generatorPath(file.Source, data)
		if err != nil {
			return nil, err
		}
		content, err := fs.ReadFile(p.FS, source)
		if err != nil {
			return nil, fmt.Errorf("read plugin generator source %q: %w\nTry: repair the plugin source", source, err)
		}
		if strings.HasSuffix(source, ".tmpl") {
			text, err := execute(string(content), data)
			if err != nil {
				return nil, err
			}
			content = []byte(text)
		}
		if path.Ext(dest) == ".go" {
			content, err = format.Source(content)
			if err != nil {
				return nil, fmt.Errorf("format plugin output %q: %w\nTry: correct the plugin template", dest, err)
			}
		}
		if err := output.Plan.Add(vfs.File{Path: dest, Data: manifest.Normalize(content), Class: file.Class, Mode: 0644, Template: "plugin/" + p.Meta.Name + "/@gen/" + spec.Name}); err != nil {
			return nil, err
		}
	}
	return output, nil
}

func execute(source string, data map[string]any) (string, error) {
	tmpl, err := template.New("plugin generator").Option("missingkey=error").Parse(source)
	if err != nil {
		return "", fmt.Errorf("parse plugin generator: %w\nTry: correct the template syntax", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render plugin generator: %w\nTry: supply valid generator arguments", err)
	}
	return buf.String(), nil
}
func generatorPath(source string, data map[string]any) (string, error) {
	value, err := execute(source, data)
	if err != nil {
		return "", err
	}
	clean, err := vfs.CleanPath(value)
	if err != nil || clean != value || strings.Split(clean, "/")[0] == ".git" || strings.Split(clean, "/")[0] == ".spinup" {
		return "", fmt.Errorf("render plugin generator path %q: unsafe path\nTry: supply clean relative paths and arguments", value)
	}
	return clean, nil
}

// RunGenerator uses the normal runner and records plugin template ownership.
// Core generators retain the runner's existing ownership rules.
func RunGenerator(ctx context.Context, g gen.Generator, args []string, flags *pflag.FlagSet, o genrun.Options) (*genrun.Result, error) {
	if g.Run == nil {
		return genrun.Run(ctx, g, args, flags, o)
	}
	original := g.Run
	var plan vfs.Plan
	g.Run = func(ctx context.Context, in gen.Input) (*gen.Output, error) {
		output, err := original(ctx, in)
		if err == nil && output != nil {
			plan = output.Plan
		}
		return output, err
	}
	result, err := genrun.Run(ctx, g, args, flags, o)
	if err != nil || o.DryRun || o.Diff {
		return result, err
	}
	hasPlugin := false
	for _, file := range plan.Files {
		hasPlugin = hasPlugin || strings.HasPrefix(file.Template, "plugin/")
	}
	if !hasPlugin {
		return result, nil
	}
	lockPath := filepath.Join(o.Root, project.LockFile)
	lock, err := manifest.Load(lockPath)
	if err != nil {
		return result, err
	}
	changed := false
	for _, file := range plan.Files {
		entry, exists := lock.Files[file.Path]
		if exists && strings.HasPrefix(file.Template, "plugin/") && entry.Template == "gen/"+g.Name {
			entry.Template = file.Template
			lock.Files[file.Path] = entry
			changed = true
		}
	}
	if changed {
		err = lock.Save(lockPath)
	}
	return result, err
}
