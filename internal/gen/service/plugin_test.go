package service

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stopyellingatme/spinup/internal/plugin"
	"github.com/stopyellingatme/spinup/internal/services"
)

func TestRenderPluginTemplates(t *testing.T) {
	t.Parallel()
	loaded := plugin.Loaded{
		Meta: plugin.Meta{Name: "service-example"},
		FS: fstest.MapFS{
			"templates/go-http/Dockerfile.tmpl": {Data: []byte("FROM scratch\nEXPOSE {{.Port}}\n")},
			"templates/go-http/main.go.tmpl":    {Data: []byte("package main\n// {{.Name}} belongs to {{.App}}\n")},
			"templates/go-http/openapi.yaml":    {Data: []byte("openapi: 3.0.0\n")},
			"templates/go-http/service.toml":    {Data: []byte("health = '/ready'\nhealthcheck = ['service', 'check']\n")},
		},
	}
	svc := services.Service{Name: "ocr", Port: 9090, DevPort: 8101, Health: "/healthz", OpenAPI: "openapi.yaml"}
	if err := applyServiceTemplateConfig(loaded.FS, "templates/go-http", &svc); err != nil {
		t.Fatal(err)
	}
	if svc.Health != "/ready" || strings.Join(svc.Healthcheck, " ") != "service check" {
		t.Fatalf("service config = %+v", svc)
	}
	plan, err := renderPluginTemplates(loaded, plugin.ServiceTemplate{Name: "go-http", Dir: "templates/go-http"}, svc, templateData{Name: "ocr", App: "todo", Port: 9090})
	if err != nil {
		t.Fatal(err)
	}
	files := plan.FS()
	main, err := fs.ReadFile(files, "services/ocr/main.go")
	if err != nil || !strings.Contains(string(main), "ocr belongs to todo") {
		t.Fatalf("main = %q, error = %v", main, err)
	}
	docker, err := fs.ReadFile(files, "services/ocr/Dockerfile")
	if err != nil || !strings.Contains(string(docker), "EXPOSE 9090") {
		t.Fatalf("Dockerfile = %q, error = %v", docker, err)
	}
	if _, err := fs.Stat(files, "services/ocr/service.toml"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("service.toml stat = %v", err)
	}
	file, ok := plan.Get("services/ocr/main.go")
	if !ok || file.Template != "plugin/service-example/@gen/service/go-http" {
		t.Fatalf("file = %+v, exists = %v", file, ok)
	}
}

func TestPluginTemplateErrors(t *testing.T) {
	t.Parallel()
	plugins := []plugin.Loaded{{Meta: plugin.Meta{Name: "service-example", ServiceTemplates: []plugin.ServiceTemplate{{Name: "go-http"}}}}}
	if _, _, ok := findServiceTemplate(plugins, "service-example", "missing"); ok {
		t.Fatal("found missing template")
	}
	err := serviceTemplateNotFound("service-example", "missing", plugins)
	if !strings.Contains(err.Error(), "service-example/go-http") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("error = %v", err)
	}
	bad := fstest.MapFS{"template/service.toml": {Data: []byte("unknown = true\n")}}
	err = applyServiceTemplateConfig(bad, "template", &services.Service{})
	if err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("error = %v", err)
	}
}

func TestPluginTemplateWithoutConfigOmitsHealthcheck(t *testing.T) {
	t.Parallel()
	svc := services.Service{Name: "ocr", Port: 8080, DevPort: 8101, Health: "/healthz"}
	if err := applyServiceTemplateConfig(fstest.MapFS{}, "template", &svc); err != nil {
		t.Fatal(err)
	}
	if len(svc.Healthcheck) != 0 {
		t.Fatalf("healthcheck = %v", svc.Healthcheck)
	}
	if table := string(appendTable(nil, svc)); strings.Contains(table, "healthcheck") {
		t.Fatalf("table contains healthcheck: %s", table)
	}
}

// TestRenderPluginTemplatesKeepsExecutableBit keeps mode 0755 for a source file with an
// execute bit, copied or rendered. Other files get 0644 (issue #37, review 6).
func TestRenderPluginTemplatesKeepsExecutableBit(t *testing.T) {
	t.Parallel()
	loaded := plugin.Loaded{
		Meta: plugin.Meta{Name: "service-example"},
		FS: fstest.MapFS{
			"t/entrypoint.sh":  {Data: []byte("#!/bin/sh\nexec /service\n"), Mode: 0o755},
			"t/run.sh.tmpl":    {Data: []byte("#!/bin/sh\necho {{.Name}}\n"), Mode: 0o700},
			"t/group.sh":       {Data: []byte("#!/bin/sh\n"), Mode: 0o654},
			"t/Dockerfile":     {Data: []byte("FROM scratch\n"), Mode: 0o644},
			"t/config.yaml":    {Data: []byte("a: 1\n"), Mode: 0o600},
			"t/main.go.tmpl":   {Data: []byte("package main\n"), Mode: 0o664},
			"t/README.md.tmpl": {Data: []byte("# {{.Name}}\n")},
		},
	}
	svc := services.Service{Name: "ocr", Port: 9090, DevPort: 8101, Health: "/healthz"}
	plan, err := renderPluginTemplates(loaded, plugin.ServiceTemplate{Name: "t", Dir: "t"}, svc, templateData{Name: "ocr", App: "todo", Port: 9090})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want fs.FileMode
	}{
		{"services/ocr/entrypoint.sh", 0o755},
		{"services/ocr/run.sh", 0o755},
		{"services/ocr/group.sh", 0o755},
		{"services/ocr/Dockerfile", 0o644},
		{"services/ocr/config.yaml", 0o644},
		{"services/ocr/main.go", 0o644},
		{"services/ocr/README.md", 0o644},
	} {
		file, ok := plan.Get(tc.path)
		if !ok {
			t.Fatalf("missing %s", tc.path)
		}
		if file.Mode != tc.want {
			t.Errorf("%s: mode = %v, want %v", tc.path, file.Mode, tc.want)
		}
	}
}
