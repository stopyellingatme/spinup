package build

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"

	run "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/services"
	"github.com/stopyellingatme/spinup/internal/ui"
)

// ImageOptions configures application image builds.
type ImageOptions struct {
	Root, App, Tag, Platform string
	Push                     bool
	Registry                 string
	Targets                  []string
	Services                 []services.Service
	PublicOrigin             string
}

// Image identifies an image and its immutable digest when it was pushed.
type Image struct {
	Name, Ref, Digest string
}

// BuildImages builds the selected application images with Docker buildx.
func BuildImages(ctx context.Context, r run.Runner, o ImageOptions) ([]Image, error) {
	if o.Root == "" || o.App == "" {
		return nil, &ui.Error{What: "Cannot build images", Why: "the project root and app name are required", Try: "run spinup build inside a spinup project"}
	}
	if o.Tag == "" {
		o.Tag = "dev"
	}
	if o.Platform == "" {
		o.Platform = "linux/amd64"
	}
	lock, err := loadBuildLock(o.Root)
	if err != nil {
		return nil, buildError(err)
	}
	embed := embedsWeb(lock)
	if len(o.Targets) == 0 {
		o.Targets = []string{"api"}
		// The deploy flow uses the same rule: a web image exists only with deploy/Dockerfile.web.
		if _, err := os.Stat(filepath.Join(o.Root, "deploy", "Dockerfile.web")); err == nil && !embed {
			o.Targets = append(o.Targets, "web")
		}
		for _, service := range o.Services {
			o.Targets = append(o.Targets, service.Name)
		}
	}
	kitDir := ""
	if o.hasTarget("api") {
		var err error
		kitDir, _, err = KitReplaceDir(filepath.Join(o.Root, "api"))
		if err != nil {
			return nil, err
		}
	}

	owner := ""
	if o.Push {
		var err error
		owner, err = moduleOwner(filepath.Join(o.Root, "api", "go.mod"))
		if err != nil {
			return nil, err
		}
		if o.Registry == "" {
			o.Registry = "ghcr.io"
		}
	}

	images := make([]Image, 0, len(o.Targets))
	for _, target := range o.Targets {
		service, ok := serviceTarget(o.Services, target)
		if target != "api" && target != "web" && !ok {
			return nil, &ui.Error{What: "Cannot build images", Why: "unknown image target " + target, Try: "pass one of: " + strings.Join(imageTargets(o.Services), ", ")}
		}
		name := o.App + "-" + target
		if ok {
			name = services.ImageName(o.App, service.Name)
		}
		ref := name + ":" + o.Tag
		if o.Push {
			ref = strings.TrimSuffix(o.Registry, "/") + "/" + owner + "/" + ref
		}
		digest, err := buildImage(ctx, r, o, target, ref, embed, kitDir)
		if err != nil {
			return nil, err
		}
		images = append(images, Image{Name: target, Ref: ref, Digest: digest})
	}
	return images, nil
}

func buildImage(ctx context.Context, r run.Runner, o ImageOptions, target, ref string, embed bool, kitDir string) (string, error) {
	dockerfile, contextDir := "deploy/Dockerfile."+target, target
	if service, ok := serviceTarget(o.Services, target); ok {
		dockerfile, contextDir = services.Dockerfile(service.Name), services.ContextDir(service.Name)
	}
	args := []string{"buildx", "build", "--platform", o.Platform, "-f", dockerfile, "-t", ref, "--build-arg"}
	if target != "web" {
		args = append(args, "VERSION="+o.Tag)
	} else {
		args = append(args, "PUBLIC_ORIGIN="+o.PublicOrigin)
	}
	if target == "api" && embed {
		args = append(args, "--build-context", "web=web")
	}
	if target == "api" && kitDir != "" {
		// A local kit replace is outside the context. BuildKit gets it as the context kit.
		args = append(args, "--build-context", "kit="+kitDir)
	}
	if !o.Push {
		args = append(args, "--load", contextDir)
		if _, err := r.Run(ctx, run.Cmd{Name: "docker", Args: args, Dir: o.Root}); err != nil {
			return "", imageError(err)
		}
		return "", nil
	}

	metadata, err := os.CreateTemp("", "spinup-buildx-*.json")
	if err != nil {
		return "", &ui.Error{What: "Cannot build image", Why: err.Error(), Try: "check the temporary directory and run the command again", Err: err}
	}
	metadataPath := metadata.Name()
	if err := metadata.Close(); err != nil {
		_ = os.Remove(metadataPath)
		return "", &ui.Error{What: "Cannot build image", Why: err.Error(), Try: "check the temporary directory and run the command again", Err: err}
	}
	defer os.Remove(metadataPath)

	args = append(args, "--provenance=mode=max", "--sbom=true", "--metadata-file", metadataPath, "--push", contextDir)
	if _, err := r.Run(ctx, run.Cmd{Name: "docker", Args: args, Dir: o.Root}); err != nil {
		return "", imageError(err)
	}
	digest, err := readDigest(metadataPath)
	if err != nil {
		return "", err
	}
	return digest, nil
}

func serviceTarget(list []services.Service, name string) (services.Service, bool) {
	for _, service := range list {
		if service.Name == name {
			return service, true
		}
	}
	return services.Service{}, false
}

func imageTargets(list []services.Service) []string {
	targets := make([]string, 0, len(list)+2)
	targets = append(targets, "api", "web")
	for _, service := range list {
		targets = append(targets, service.Name)
	}
	return targets
}

func (o ImageOptions) hasTarget(name string) bool {
	for _, t := range o.Targets {
		if t == name {
			return true
		}
	}
	return false
}

func moduleOwner(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", &ui.Error{What: "Cannot build pushed images", Why: err.Error(), Try: "check api/go.mod and run spinup build again", Err: err}
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || fields[0] != "module" {
			continue
		}
		parts := strings.Split(strings.Trim(fields[1], "\"`"), "/")
		if len(parts) >= 3 && parts[0] == "github.com" && parts[1] != "" {
			return parts[1], nil
		}
		break
	}
	if err := scanner.Err(); err != nil {
		return "", &ui.Error{What: "Cannot build pushed images", Why: err.Error(), Try: "check api/go.mod and run spinup build again", Err: err}
	}
	return "", &ui.Error{What: "Cannot build pushed images", Why: "the API module does not identify a GitHub owner", Try: "use a github.com/<owner>/... module path or pass a registry image name"}
}

func imageError(err error) error {
	return &ui.Error{What: "Cannot build image", Why: err.Error(), Try: "check Docker buildx output and run spinup build again", Err: err}
}

func imageWarning(o ImageOptions) string {
	if o.Push {
		return ""
	}
	return "Attestations require --push and were not created."
}
