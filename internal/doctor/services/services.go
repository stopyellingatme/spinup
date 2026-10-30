// Package services checks the declared sidecar services in a spinup project.
package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"github.com/stopyellingatme/spinup/internal/project"
	servicecontract "github.com/stopyellingatme/spinup/internal/services"
	"go.yaml.in/yaml/v3"
)

func init() {
	doctor.Register(doctor.Check{ID: "services", Title: "Sidecar services", Scope: doctor.Local, Run: runCheck})
}

func runCheck(_ context.Context, c *doctor.Context) doctor.Result {
	if c.Root == "" || c.Quick {
		return doctor.Result{Status: doctor.Skip, Message: "requires a project and a full check"}
	}
	config, err := project.Load(c.Root)
	if err != nil {
		return doctor.Result{Status: doctor.Fail, Message: "cannot read spinup.toml", Try: "correct spinup.toml"}
	}
	if len(config.Services) == 0 {
		return doctor.Result{Status: doctor.OK, Message: "no sidecar services are declared"}
	}
	var findings []finding
	if err := servicecontract.Validate(config.Services, nil); err != nil {
		findings = append(findings, finding{doctor.Fail, "spinup.toml has invalid services: " + err.Error(), "correct the [[services]] tables in spinup.toml"})
	}
	for _, svc := range config.Services {
		if servicecontract.ValidName(svc.Name) != nil {
			continue
		}
		if !exists(filepath.Join(c.Root, filepath.FromSlash(servicecontract.Dockerfile(svc.Name)))) {
			findings = append(findings, finding{doctor.Fail, servicecontract.Dockerfile(svc.Name) + " does not exist", "add a Dockerfile for service " + svc.Name})
		}
		if svc.OpenAPI != "" {
			if !filepath.IsLocal(filepath.FromSlash(svc.OpenAPI)) {
				continue
			}
			openapi := filepath.Join(c.Root, filepath.FromSlash(servicecontract.ContextDir(svc.Name)), filepath.FromSlash(svc.OpenAPI))
			if !exists(openapi) {
				findings = append(findings, finding{doctor.Fail, "services/" + svc.Name + "/" + svc.OpenAPI + " does not exist", "add the OpenAPI file or remove openapi from spinup.toml"})
			} else if !exists(filepath.Join(c.Root, filepath.FromSlash(servicecontract.ClientDir(svc.Name)), "client.gen.go")) {
				findings = append(findings, finding{doctor.Warn, "generated Go client is missing for service " + svc.Name, "spinup g client"})
			}
		}
	}
	envNames := make([]string, 0, len(config.Envs))
	for name := range config.Envs {
		envNames = append(envNames, name)
	}
	sort.Strings(envNames)
	for _, env := range envNames {
		missingSecrets, err := checkSecrets(c.Root, env, config.Services)
		if err != nil {
			findings = append(findings, finding{doctor.Fail, "cannot inspect encrypted secret keys for environment " + env + ": " + err.Error(), "check secrets/" + env + ".enc.yaml"})
			continue
		}
		for _, name := range missingSecrets {
			findings = append(findings, finding{doctor.Warn, "secret " + name + " is not declared in secrets/" + env + ".enc.yaml for environment " + env, "add " + name + " to secrets/" + env + ".enc.yaml"})
		}
	}
	if len(findings) == 0 {
		return doctor.Result{Status: doctor.OK, Message: "declared sidecar services are present"}
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].status != findings[j].status {
			return findings[i].status > findings[j].status
		}
		return findings[i].message < findings[j].message
	})
	worst := doctor.Warn
	lines := make([]string, 0, len(findings))
	for _, item := range findings {
		if item.status > worst {
			worst = item.status
		}
		lines = append(lines, item.message+"\nTry: "+item.try)
	}
	return doctor.Result{Status: worst, Message: strings.Join(lines, "\n")}
}

type finding struct {
	status  doctor.Status
	message string
	try     string
}

func exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// checkSecrets returns the declared secrets that secrets/<env>.enc.yaml does not name.
// A missing file for the environment means that every declared secret is missing.
func checkSecrets(root, env string, services []servicecontract.Service) ([]string, error) {
	required := map[string]bool{}
	for _, svc := range services {
		for _, name := range svc.Secrets {
			required[name] = true
		}
	}
	if len(required) == 0 {
		return nil, nil
	}
	file := filepath.Join(root, "secrets", env+".enc.yaml")
	data, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(file), err)
	}
	if err == nil {
		var document yaml.Node
		if err := yaml.Unmarshal(data, &document); err != nil {
			return nil, fmt.Errorf("parse %s: invalid YAML", filepath.Base(file))
		}
		if len(document.Content) > 0 && document.Content[0].Kind == yaml.MappingNode {
			mapping := document.Content[0]
			for i := 0; i+1 < len(mapping.Content); i += 2 {
				key := mapping.Content[i]
				if key.Kind == yaml.ScalarNode {
					delete(required, key.Value)
				}
			}
		}
	}
	names := make([]string, 0, len(required))
	for name := range required {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
