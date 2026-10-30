// Package serviceclient generates typed Go clients for sidecar services.
package serviceclient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/run"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/services"
)

func init() {
	run.RegisterPostStep(gen.PostServiceClients, postStep)
}

func postStep(ctx context.Context, root string, _ []string, runner exec.Runner) error {
	config, err := project.Load(root)
	if err != nil {
		return fmt.Errorf("load service configuration: %w\nTry: correct spinup.toml, then run spinup g client", err)
	}
	for _, service := range config.Services {
		if service.OpenAPI == "" {
			continue
		}
		if err := generate(ctx, root, service, runner); err != nil {
			return err
		}
	}
	return nil
}

func generate(ctx context.Context, root string, service services.Service, runner exec.Runner) error {
	openAPIPath := filepath.Join(root, filepath.FromSlash(services.ContextDir(service.Name)), filepath.FromSlash(service.OpenAPI))
	if _, err := os.Stat(openAPIPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("service %q OpenAPI file %q does not exist\nTry: create services/%s/%s, or remove openapi from spinup.toml", service.Name, service.OpenAPI, service.Name, service.OpenAPI)
		}
		return fmt.Errorf("inspect service %q OpenAPI file: %w\nTry: check services/%s/%s permissions", service.Name, err, service.Name, service.OpenAPI)
	}

	clientDir := filepath.Join(root, filepath.FromSlash(services.ClientDir(service.Name)))
	if err := os.MkdirAll(clientDir, 0755); err != nil {
		return fmt.Errorf("create service %q client directory: %w\nTry: check api/internal/services permissions", service.Name, err)
	}
	configPath := filepath.Join(clientDir, "oapi-codegen.yaml")
	if err := os.WriteFile(configPath, []byte(codegenConfig(service.Name)), 0644); err != nil {
		return fmt.Errorf("write service %q oapi-codegen configuration: %w\nTry: check %s permissions", service.Name, err, services.ClientDir(service.Name))
	}
	if err := writeScaffold(clientDir, service.Name); err != nil {
		return err
	}

	result, err := runner.Run(ctx, exec.Cmd{
		Name: "go",
		Args: []string{"tool", "-modfile=tools.mod", "oapi-codegen", "-config", filepath.ToSlash(filepath.Join("internal", "services", service.Name, "oapi-codegen.yaml")), filepath.ToSlash(filepath.Join("..", "services", service.Name, service.OpenAPI))},
		Dir:  filepath.Join(root, "api"),
	})
	if err != nil {
		return fmt.Errorf("run oapi-codegen for service %q: %w\n%s%s\nTry: correct the service OpenAPI file, then run spinup g client", service.Name, err, result.Stdout, result.Stderr)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("oapi-codegen for service %q exited with code %d\n%s%s\nTry: correct the service OpenAPI file, then run spinup g client", service.Name, result.ExitCode, result.Stdout, result.Stderr)
	}
	if err := moveGeneratedClient(filepath.Join(root, "api"), clientDir); err != nil {
		return fmt.Errorf("store generated client for service %q: %w\nTry: check %s permissions", service.Name, err, services.ClientDir(service.Name))
	}
	return nil
}

func codegenConfig(name string) string {
	return "package: " + name + "\noutput: client.gen.go\ngenerate:\n  client: true\n  models: true\n"
}

func writeScaffold(clientDir, name string) error {
	path := filepath.Join(clientDir, name+".go")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect service %q client scaffold: %w\nTry: check %s permissions", name, err, filepath.ToSlash(path))
	}
	if err := os.WriteFile(path, []byte(scaffold(name)), 0644); err != nil {
		return fmt.Errorf("write service %q client scaffold: %w\nTry: check %s permissions", name, err, filepath.ToSlash(path))
	}
	return nil
}

func moveGeneratedClient(apiDir, clientDir string) error {
	source := filepath.Join(apiDir, "client.gen.go")
	if _, err := os.Stat(source); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return os.Rename(source, filepath.Join(clientDir, "client.gen.go"))
}

func scaffold(name string) string {
	return fmt.Sprintf(`// Package %[1]s provides the typed client for the %[1]s service.
package %[1]s

import (
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Config configures the %[1]s service client.
type Config struct {
	URL string `+"`env:\""+services.URLEnv(name)+"\" required:\"true\"`"+`
}

// New creates a %[1]s service client.
func New(cfg Config) (*ClientWithResponses, error) {
	client := &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
		Timeout:   30 * time.Second,
	}
	return NewClientWithResponses(cfg.URL, WithHTTPClient(client))
}
`, name)
}
