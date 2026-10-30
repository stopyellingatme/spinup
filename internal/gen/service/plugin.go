package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"text/template"

	"github.com/pelletier/go-toml/v2"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/plugin/pack"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/services"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

type serviceTemplateConfig struct {
	Healthcheck []string `toml:"healthcheck"`
	Health      string   `toml:"health"`
}

func planPlugin(ctx context.Context, in gen.Input, src []byte, config *project.Config, name, pluginName, templateName string) (*gen.Output, error) {
	port, _ := in.Flags.GetInt("port")
	devPort, _ := in.Flags.GetInt("dev-port")
	if !in.Flags.Changed("dev-port") {
		devPort = nextDevPort(config.Services)
	}
	noOpenAPI, _ := in.Flags.GetBool("no-openapi")
	svc := services.Service{Name: name, Port: port, DevPort: devPort, Health: "/healthz"}
	if !noOpenAPI {
		svc.OpenAPI = "openapi.yaml"
	}
	plugins, err := plugin.Resolve(ctx, plugin.Options{Root: in.Root})
	if err != nil {
		return nil, fmt.Errorf("resolve plugin service template: %w", err)
	}
	loaded, spec, ok := findServiceTemplate(plugins, pluginName, templateName)
	if !ok {
		return nil, serviceTemplateNotFound(pluginName, templateName, plugins)
	}
	if err := applyServiceTemplateConfig(loaded.FS, spec.Dir, &svc); err != nil {
		return nil, err
	}
	if err := services.Validate(append(config.Services, svc), nil); err != nil {
		return nil, err
	}
	p, err := renderPluginTemplates(loaded, spec, svc, templateData{Name: name, App: config.App.Name, Port: port})
	if err != nil {
		return nil, err
	}
	if err := p.Add(vfs.File{Path: project.ConfigFile, Data: appendTable(src, svc), Mode: 0o644, Class: string(manifest.Scaffold)}); err != nil {
		return nil, fmt.Errorf("plan spinup.toml: %w\nTry: report this as a spinup bug", err)
	}
	next := []string{"spinup dev"}
	if !noOpenAPI {
		next = append([]string{"spinup g client"}, next...)
	}
	return &gen.Output{Plan: p, Edits: []string{project.ConfigFile}, Next: next}, nil
}

func findServiceTemplate(plugins []plugin.Loaded, pluginName, templateName string) (plugin.Loaded, plugin.ServiceTemplate, bool) {
	for _, loaded := range plugins {
		if loaded.Meta.Name != pluginName {
			continue
		}
		for _, spec := range loaded.Meta.ServiceTemplates {
			if spec.Name == templateName {
				return loaded, spec, true
			}
		}
	}
	return plugin.Loaded{}, plugin.ServiceTemplate{}, false
}

func serviceTemplateNotFound(pluginName, templateName string, plugins []plugin.Loaded) error {
	available := make([]string, 0)
	for _, loaded := range plugins {
		for _, spec := range loaded.Meta.ServiceTemplates {
			available = append(available, loaded.Meta.Name+"/"+spec.Name)
		}
	}
	slices.Sort(available)
	list := "none"
	if len(available) > 0 {
		list = strings.Join(available, ", ")
	}
	return fmt.Errorf("cannot generate service: plugin template %q does not exist; available templates: %s\nTry: use one of the listed plugin templates", pluginName+"/"+templateName, list)
}

func applyServiceTemplateConfig(fsys fs.FS, dir string, svc *services.Service) error {
	data, err := fs.ReadFile(fsys, path.Join(dir, "service.toml"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read plugin service configuration: %w\nTry: repair the plugin service template", err)
	}
	var config serviceTemplateConfig
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&config); err != nil {
		return fmt.Errorf("read plugin service configuration: %w\nTry: correct service.toml in the plugin", err)
	}
	if config.Health != "" {
		svc.Health = config.Health
	}
	if config.Healthcheck != nil {
		svc.Healthcheck = config.Healthcheck
	}
	return nil
}

func renderPluginTemplates(loaded plugin.Loaded, spec plugin.ServiceTemplate, svc services.Service, data templateData) (vfs.Plan, error) {
	var result vfs.Plan
	base := strings.TrimSuffix(spec.Dir, "/")
	err := fs.WalkDir(loaded.FS, base, func(source string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(source, base+"/")
		if rel == "service.toml" || svc.OpenAPI == "" && rel == "openapi.yaml" {
			return nil
		}
		output := path.Join(services.ContextDir(svc.Name), rel)
		if err := pack.CheckOutputPath(output); err != nil {
			return err
		}
		if !strings.HasPrefix(output, services.ContextDir(svc.Name)+"/") {
			return fmt.Errorf("render plugin service template: output %q escapes the service directory\nTry: repair the plugin service template", output)
		}
		content, err := fs.ReadFile(loaded.FS, source)
		if err != nil {
			return err
		}
		// Keep the executable bit, so that a script such as entrypoint.sh still runs.
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		if strings.HasSuffix(rel, ".tmpl") {
			tmpl, err := template.New(source).Option("missingkey=error").Parse(string(content))
			if err != nil {
				return fmt.Errorf("parse plugin service template %q: %w\nTry: correct the template syntax", source, err)
			}
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, data); err != nil {
				return fmt.Errorf("render plugin service template %q: %w\nTry: correct the template data", source, err)
			}
			content = buf.Bytes()
			rel = strings.TrimSuffix(rel, ".tmpl")
			output = path.Join(services.ContextDir(svc.Name), rel)
			if err := pack.CheckOutputPath(output); err != nil {
				return err
			}
		}
		return result.Add(vfs.File{Path: output, Data: manifest.Normalize(content), Mode: mode, Class: string(manifest.Scaffold), Template: "plugin/" + loaded.Meta.Name + "/@gen/service/" + spec.Name})
	})
	if err != nil {
		return vfs.Plan{}, fmt.Errorf("render plugin service template %q: %w\nTry: repair the plugin service template", loaded.Meta.Name+"/"+spec.Name, err)
	}
	return result, nil
}
