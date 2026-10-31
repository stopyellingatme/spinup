package service

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/spf13/pflag"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/services"
	"github.com/stopyellingatme/spinup/internal/stackversions"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

//go:embed all:templates
var templates embed.FS

func init() {
	registry.Register(gen.Generator{
		Name: "service", Short: "Add a sidecar service",
		Usage: "<name> --lang rust|python|node|<plugin>/<template> [--port 8080] [--dev-port N] [--public /svc/<name>] [--auth none|session] [--no-openapi]",
		Flags: flags, Run: plan,
	})
}

func flags(fs *pflag.FlagSet) {
	fs.String("lang", "", "service language or plugin template (required)")
	fs.Int("port", 8080, "container port")
	fs.Int("dev-port", 0, "host port (default: first unused port from 8101)")
	fs.String("public", "", "public URL prefix")
	fs.String("auth", "none", "public route authentication: none or session")
	fs.Bool("no-openapi", false, "omit the OpenAPI file and Go client")
}

func plan(ctx context.Context, in gen.Input) (*gen.Output, error) {
	if len(in.Args) != 1 {
		return nil, fmt.Errorf("cannot generate service: expected one name\nTry: spinup g service ocr --lang rust")
	}
	name := in.Args[0]
	if err := services.ValidName(name); err != nil {
		return nil, err
	}
	if in.Flags == nil {
		return nil, fmt.Errorf("cannot generate service: --lang is required\nTry: spinup g service %s --lang rust", name)
	}
	lang, _ := in.Flags.GetString("lang")
	pluginName, templateName, pluginTemplate := strings.Cut(lang, "/")
	if lang != "rust" && lang != "python" && lang != "node" && (!pluginTemplate || pluginName == "" || templateName == "" || strings.Contains(templateName, "/")) {
		return nil, fmt.Errorf("cannot generate service: --lang is required and must be rust, python, or node, got %q\nTry: spinup g service %s --lang rust", lang, name)
	}
	dir := services.ContextDir(name)
	if _, err := os.Lstat(filepath.Join(in.Root, filepath.FromSlash(dir))); err == nil {
		return nil, fmt.Errorf("cannot generate service %q: %s already exists\nTry: choose another service name", name, dir)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect %s: %w\nTry: check the directory permissions", dir, err)
	}
	src, err := os.ReadFile(filepath.Join(in.Root, project.ConfigFile))
	if err != nil {
		return nil, fmt.Errorf("read spinup.toml: %w\nTry: check the file and its permissions", err)
	}
	config, err := project.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("read service declarations: %w\nTry: correct spinup.toml", err)
	}
	for _, existing := range config.Services {
		if existing.Name == name {
			return nil, fmt.Errorf("cannot generate service %q: spinup.toml already declares the name\nTry: choose another service name", name)
		}
	}
	if pluginTemplate {
		return planPlugin(ctx, in, src, config, name, pluginName, templateName)
	}
	port, _ := in.Flags.GetInt("port")
	devPort, _ := in.Flags.GetInt("dev-port")
	if !in.Flags.Changed("dev-port") {
		devPort = nextDevPort(config.Services)
	}
	noOpenAPI, _ := in.Flags.GetBool("no-openapi")
	svc := services.Service{Name: name, Port: port, DevPort: devPort, Health: "/healthz"}
	if in.Flags.Changed("public") {
		svc.Public, _ = in.Flags.GetString("public")
		if svc.Public == "" {
			return nil, fmt.Errorf("cannot generate service: --public requires a URL prefix\nTry: pass --public /svc/%s", name)
		}
	}
	if in.Flags.Changed("auth") {
		svc.Auth, _ = in.Flags.GetString("auth")
	}
	if !noOpenAPI {
		svc.OpenAPI = "openapi.yaml"
	}
	switch lang {
	case "python":
		svc.Healthcheck = []string{"/app/.venv/bin/python", "-m", "service.healthcheck"}
	case "node":
		svc.Healthcheck = []string{"/usr/local/bin/node", "/app/src/healthcheck.ts"}
	default:
		svc.Healthcheck = []string{"/service", "healthcheck"}
	}
	if err := services.Validate(append(config.Services, svc), nil); err != nil {
		return nil, err
	}
	if svc.AuthMode() == services.AuthSession && !slices.Contains(config.Modules, "auth") {
		return nil, fmt.Errorf("cannot generate service: --auth session needs the auth module\nTry: add the auth module, or pass --auth none")
	}
	stack, err := stackversions.Load()
	if err != nil {
		return nil, fmt.Errorf("load service versions: %w\nTry: report this as a spinup bug", err)
	}
	data := templateData{Name: name, App: config.App.Name, Port: port}
	for _, image := range []struct {
		name string
		ref  *string
	}{
		{"rust", &data.RustImage}, {"distroless-static-debian13", &data.DistrolessImage},
		{"python", &data.PythonImage}, {"uv", &data.UVImage},
		{"node", &data.NodeImage},
	} {
		pin, err := stack.Image(image.name)
		if err != nil {
			return nil, fmt.Errorf("load service image: %w\nTry: report this as a spinup bug", err)
		}
		*image.ref = pin.Ref()
	}
	p, err := renderTemplates(lang, svc, data)
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

func nextDevPort(list []services.Service) int {
	used := make(map[int]bool, len(list))
	for _, svc := range list {
		used[svc.DevPort] = true
	}
	for port := 8101; port <= 65535; port++ {
		if !used[port] && !slices.Contains(services.DefaultDevPorts, port) {
			return port
		}
	}
	return 0 // Validate reports that no valid port remains.
}

type templateData struct {
	Name, App                                                   string
	Port                                                        int
	RustImage, DistrolessImage, PythonImage, UVImage, NodeImage string
}

func renderTemplates(lang string, svc services.Service, data templateData) (vfs.Plan, error) {
	var p vfs.Plan
	base := "templates/" + lang
	err := fs.WalkDir(templates, base, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(name, base+"/")
		if svc.OpenAPI == "" && rel == "openapi.yaml" {
			return nil
		}
		content, err := templates.ReadFile(name)
		if err != nil {
			return err
		}
		if strings.HasSuffix(rel, ".tmpl") {
			tmpl, err := template.New(name).Option("missingkey=error").Parse(string(content))
			if err != nil {
				return err
			}
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, data); err != nil {
				return err
			}
			content = buf.Bytes()
			rel = strings.TrimSuffix(rel, ".tmpl")
		}
		return p.Add(vfs.File{Path: path.Join(services.ContextDir(svc.Name), rel), Data: content, Mode: 0o644, Class: string(manifest.Scaffold)})
	})
	if err != nil {
		return vfs.Plan{}, fmt.Errorf("render %s service: %w\nTry: report this as a spinup bug", lang, err)
	}
	return p, nil
}

func appendTable(src []byte, svc services.Service) []byte {
	var buf bytes.Buffer
	buf.Write(src)
	if len(src) > 0 && src[len(src)-1] != '\n' {
		buf.WriteByte('\n')
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte("\n\n")) {
		buf.WriteByte('\n')
	}
	fmt.Fprintf(&buf, "[[services]]\nname = %q\nport = %d\ndev_port = %d\nhealth = %q\n", svc.Name, svc.Port, svc.DevPort, svc.Health)
	if svc.Public != "" {
		fmt.Fprintf(&buf, "public = %q\n", svc.Public)
	}
	if svc.Auth != "" {
		fmt.Fprintf(&buf, "auth = %q\n", svc.Auth)
	}
	if svc.OpenAPI != "" {
		fmt.Fprintf(&buf, "openapi = %q\n", svc.OpenAPI)
	}
	if len(svc.Healthcheck) > 0 {
		buf.WriteString("healthcheck = [")
		for i, arg := range svc.Healthcheck {
			if i > 0 {
				buf.WriteString(", ")
			}
			fmt.Fprintf(&buf, "%q", arg)
		}
		buf.WriteString("]\n")
	}
	return buf.Bytes()
}
