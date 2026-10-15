package newapp

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/plugin/pack"
	pluginrender "github.com/stopyellingatme/spinup/internal/plugin/render"
	"github.com/stopyellingatme/spinup/internal/render"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/templates"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// addPlugins resolves sources in a temporary project before any application file is written.
func addPlugins(ctx context.Context, o Options, result *Result, prompter Prompter) error {
	if len(o.Plugins) == 0 {
		return nil
	}
	temp, err := os.MkdirTemp("", "spinup-new-plugins-")
	if err != nil {
		return creationError(err)
	}
	defer os.RemoveAll(temp)
	cfg, ok := result.Plan.Get("spinup.toml")
	if !ok {
		return fmt.Errorf("add plugins: project template is missing\nTry: reinstall spinup")
	}
	if err := os.WriteFile(filepath.Join(temp, "spinup.toml"), cfg.Data, 0600); err != nil {
		return creationError(err)
	}
	if err := result.Lock.Save(filepath.Join(temp, "spinup.lock")); err != nil {
		return err
	}
	var out bytes.Buffer
	m := &pack.Manager{Root: temp, PathRoot: result.Dir, CLIVersion: o.Version, Trust: o.Trust, Offline: o.Offline, Out: &out}
	if o.Interactive && !o.Trust && prompter != nil {
		m.Interactive = true
		m.In = &trustReader{ctx: ctx, prompter: prompter, out: &out}
	}
	for _, value := range o.Plugins {
		if m.Interactive {
			m.In = &trustReader{ctx: ctx, prompter: prompter, out: &out}
		}
		source, version := splitPlugin(value)
		if strings.HasPrefix(source, "path:") {
			absolute, err := filepath.Abs(strings.TrimPrefix(source, "path:"))
			if err != nil {
				return creationError(err)
			}
			relative, err := filepath.Rel(result.Dir, absolute)
			if err != nil {
				return creationError(err)
			}
			source = "path:" + filepath.ToSlash(relative)
		}
		if err := m.Add(ctx, source, version); err != nil {
			return err
		}
	}
	loaded, err := plugin.Resolve(ctx, plugin.Options{Root: temp, Offline: true})
	if err != nil {
		return err
	}
	base := &templates.Version{Version: result.Lock.TemplateVersion, Packs: templates.Embedded(), Stack: nil}
	version, names, err := pluginrender.Overlay(base, result.Packs, loaded)
	if err != nil {
		return err
	}
	var packs []*templates.Pack
	for _, name := range names {
		p, err := templates.Load(version.Packs, name)
		if err != nil {
			return err
		}
		packs = append(packs, p)
	}
	questions, err := Questions(packs)
	if err != nil {
		return err
	}
	more := o
	more.Answers = maps.Clone(result.Lock.Answers)
	for key, value := range o.Answers {
		more.Answers[key] = value
	}
	answers, _, err := completeAnswers(ctx, more, questions, prompter)
	if err != nil {
		return err
	}
	// Render all packs together so duplicate output paths fail before application.
	version.Stack = stackversions.MustLoad()
	plan, err := render.Render(render.Input{Packs: packs, Answers: answers, Stack: version.Stack, TemplateVersion: version.Version})
	if err != nil {
		return err
	}
	plan = pluginrender.Prefix(version.Packs, plan)
	// Preserve the local kit replacement already applied to the core plan.
	if result.KitReplace != "" {
		original, _ := result.Plan.Get("api/go.mod")
		for i := range plan.Files {
			if plan.Files[i].Path == original.Path {
				plan.Files[i] = original
			}
		}
	}
	for _, name := range []string{plugin.AllowlistFile, ".spinup/plugins.history"} {
		data, err := os.ReadFile(filepath.Join(temp, name))
		if err != nil {
			return creationError(err)
		}
		if err := plan.Add(vfs.File{Path: name, Data: data, Class: "seed", Mode: 0644}); err != nil {
			return err
		}
	}
	result.Lock.Answers = answers
	result.Lock.Plugins = pluginrender.Plugins(version.Packs)
	result.Lock.Packs = names
	for _, file := range plan.Files {
		if file.Path == plugin.AllowlistFile || file.Path == ".spinup/plugins.history" {
			continue
		}
		result.Lock.Files[file.Path] = manifest.FileEntry{Class: manifest.Class(file.Class), Template: file.Template, Hash: manifest.Hash(file.Data), Mode: fmt.Sprintf("%04o", file.Mode.Perm())}
	}
	// Declarations are user configuration. Keep the raw core hash as their merge
	// base, as plugin add does for an existing app. Render never depends on Source.
	declared, err := os.ReadFile(filepath.Join(temp, "spinup.toml"))
	if err != nil {
		return creationError(err)
	}
	for i := range plan.Files {
		if plan.Files[i].Path == "spinup.toml" {
			plan.Files[i].Data = declared
		}
	}

	result.Plan = plan
	result.Packs = names
	return result.Lock.Validate()
}

func splitPlugin(value string) (string, string) {
	index := strings.LastIndex(value, "@")
	if index > strings.Index(value, ":") && index >= 0 && !strings.Contains(value[index+1:], "/") {
		return value[:index], value[index+1:]
	}
	return value, ""
}

type trustReader struct {
	ctx      context.Context
	prompter Prompter
	out      *bytes.Buffer
	answer   *strings.Reader
}

func (r *trustReader) Read(p []byte) (int, error) {
	if r.answer == nil {
		value, err := r.prompter.Ask(r.ctx, templates.Question{Key: "trust", Type: "bool", Prompt: r.out.String() + "\nTrust this plugin?"}, false)
		if err != nil {
			return 0, err
		}
		if accepted, _ := value.(bool); accepted {
			r.answer = strings.NewReader("yes\n")
		} else {
			r.answer = strings.NewReader("no\n")
		}
	}
	return r.answer.Read(p)
}
