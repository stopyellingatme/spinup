// Package stackversions loads the pinned stack version manifest.
package stackversions

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

//go:embed stackversions.yaml
var embedded []byte

var (
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	shaPattern    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Image identifies a pinned container image.
type Image struct {
	Image  string `yaml:"image"`
	Tag    string `yaml:"tag"`
	Digest string `yaml:"digest"`
}

// Ref returns the immutable image reference.
func (i Image) Ref() string {
	return i.Image + ":" + i.Tag + "@" + i.Digest
}

// Asset identifies a binary release asset and its SHA-256 digest.
type Asset struct {
	Name   string `yaml:"name"`
	SHA256 string `yaml:"sha256"`
}

// Binary identifies a downloadable binary release.
type Binary struct {
	Version   string            `yaml:"version"`
	Repo      string            `yaml:"repo"`
	Assets    map[string]Asset  `yaml:"assets"`
	Checksums map[string]string `yaml:"checksums"`
}

// Action identifies a GitHub Action tag and its immutable commit SHA.
type Action struct {
	Tag string `yaml:"tag"`
	SHA string `yaml:"sha"`
}

// Uses returns a GitHub Actions uses expression with a readable tag comment.
func (a Action) Uses(name string) string {
	return name + "@" + a.SHA + " # " + a.Tag
}

// Stack contains every pinned dependency used by spinup and generated projects.
type Stack struct {
	Schema   int    `yaml:"schema"`
	Verified string `yaml:"verified"`
	Go       struct {
		Toolchain string   `yaml:"toolchain"`
		Min       string   `yaml:"min"`
		Notes     []string `yaml:"notes"`
	} `yaml:"go"`
	GoModules map[string]string `yaml:"go_modules"`
	GoTools   map[string]string `yaml:"go_tools"`
	NPM       map[string]string `yaml:"npm"`
	Apt       map[string]string `yaml:"apt"`
	Tools     map[string]string `yaml:"tools"`
	Images    map[string]Image  `yaml:"images"`
	Binaries  map[string]Binary `yaml:"binaries"`
	Actions   map[string]Action `yaml:"actions"`
	// Crates, PythonPackages, and NodePackages pin the sidecar service templates
	// (ADR 0022, ADR 0023).
	Crates         map[string]string `yaml:"crates"`
	PythonPackages map[string]string `yaml:"python_packages"`
	NodePackages   map[string]string `yaml:"node_packages"`
	// DigitalOcean holds the droplet defaults of `spinup deploy digital-ocean` (ADR 0017).
	DigitalOcean DigitalOcean `yaml:"digitalocean"`
}

// DigitalOcean holds the defaults for droplets that spinup creates. Validate does not require it:
// template archives made before ADR 0017 have no digitalocean section. Provisioning reads the
// manifest compiled into the CLI.
type DigitalOcean struct {
	Image          string `yaml:"image"`
	Region         string `yaml:"region"`
	SizeProduction string `yaml:"size_production"`
	SizeDefault    string `yaml:"size_default"`
	IPDNS          string `yaml:"ip_dns"`
}

// Parse decodes and validates a stack version manifest.
func Parse(data []byte) (*Stack, error) {
	var stack Stack
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&stack); err != nil {
		return nil, fmt.Errorf("parse stack versions: %w", err)
	}
	if err := stack.Validate(); err != nil {
		return nil, err
	}
	return &stack, nil
}

// Load returns the embedded stack version manifest.
func Load() (*Stack, error) {
	return Parse(embedded)
}

// MustLoad returns the embedded stack version manifest or panics when it is invalid.
func MustLoad() *Stack {
	stack, err := Load()
	if err != nil {
		panic(err)
	}
	return stack
}

// Validate reports every invalid stack version manifest field.
func (s *Stack) Validate() error {
	var problems []error
	if s.Schema != 1 {
		problems = append(problems, fmt.Errorf("schema must be 1, got %d", s.Schema))
	}
	if strings.TrimSpace(s.Verified) == "" {
		problems = append(problems, errors.New("verified must not be empty"))
	}
	if !strings.HasPrefix(s.Go.Toolchain, "go") {
		problems = append(problems, fmt.Errorf("go.toolchain must start with go, got %q", s.Go.Toolchain))
	}
	if strings.TrimSpace(s.Go.Min) == "" {
		problems = append(problems, errors.New("go.min must not be empty"))
	}
	problems = append(problems, validateVersions("go_modules", s.GoModules)...)
	problems = append(problems, validateVersions("go_tools", s.GoTools)...)
	problems = append(problems, validateVersions("npm", s.NPM)...)
	problems = append(problems, validateVersions("apt", s.Apt)...)
	problems = append(problems, validateVersions("tools", s.Tools)...)
	problems = append(problems, validateVersions("crates", s.Crates)...)
	problems = append(problems, validateVersions("python_packages", s.PythonPackages)...)
	problems = append(problems, validateVersions("node_packages", s.NodePackages)...)

	for _, name := range sortedKeys(s.Images) {
		image := s.Images[name]
		if strings.TrimSpace(image.Image) == "" {
			problems = append(problems, fmt.Errorf("images.%s.image must not be empty", name))
		}
		if strings.TrimSpace(image.Tag) == "" {
			problems = append(problems, fmt.Errorf("images.%s.tag must not be empty", name))
		}
		if !digestPattern.MatchString(image.Digest) {
			problems = append(problems, fmt.Errorf("images.%s.digest is invalid", name))
		}
	}
	for _, name := range sortedKeys(s.Binaries) {
		binary := s.Binaries[name]
		if strings.TrimSpace(binary.Version) == "" {
			problems = append(problems, fmt.Errorf("binaries.%s.version must not be empty", name))
		}
		if strings.TrimSpace(binary.Repo) == "" {
			problems = append(problems, fmt.Errorf("binaries.%s.repo must not be empty", name))
		}
		for _, platform := range sortedKeys(binary.Assets) {
			asset := binary.Assets[platform]
			if strings.TrimSpace(asset.Name) == "" {
				problems = append(problems, fmt.Errorf("binaries.%s.assets.%s.name must not be empty", name, platform))
			}
			if !sha256Pattern.MatchString(asset.SHA256) {
				problems = append(problems, fmt.Errorf("binaries.%s.assets.%s.sha256 is invalid", name, platform))
			}
		}
	}
	for _, name := range sortedKeys(s.Actions) {
		action := s.Actions[name]
		if strings.TrimSpace(action.Tag) == "" {
			problems = append(problems, fmt.Errorf("actions.%s.tag must not be empty", name))
		}
		if !shaPattern.MatchString(action.SHA) {
			problems = append(problems, fmt.Errorf("actions.%s.sha is invalid", name))
		}
	}
	return errors.Join(problems...)
}

// Image returns the named pinned container image.
func (s *Stack) Image(name string) (Image, error) {
	image, ok := s.Images[name]
	if !ok {
		return Image{}, fmt.Errorf("stack image %q was not found", name)
	}
	return image, nil
}

// NPMVersion returns the named pinned NPM package version.
func (s *Stack) NPMVersion(pkg string) (string, error) {
	version, ok := s.NPM[pkg]
	if !ok {
		return "", fmt.Errorf("stack NPM package %q was not found", pkg)
	}
	return version, nil
}

// GoModule returns the named pinned Go module version.
func (s *Stack) GoModule(path string) (string, error) {
	version, ok := s.GoModules[path]
	if !ok {
		return "", fmt.Errorf("stack Go module %q was not found", path)
	}
	return version, nil
}

// Action returns the named pinned GitHub Action.
func (s *Stack) Action(name string) (Action, error) {
	action, ok := s.Actions[name]
	if !ok {
		return Action{}, fmt.Errorf("stack action %q was not found", name)
	}
	return action, nil
}

// Asset returns a binary release asset for the named platform.
func (s *Stack) Asset(binary, platform string) (Asset, error) {
	b, ok := s.Binaries[binary]
	if !ok {
		return Asset{}, fmt.Errorf("stack binary %q was not found", binary)
	}
	asset, ok := b.Assets[platform]
	if !ok {
		return Asset{}, fmt.Errorf("stack binary %q has no asset for platform %q", binary, platform)
	}
	return asset, nil
}

func validateVersions(group string, versions map[string]string) []error {
	var problems []error
	for _, name := range sortedKeys(versions) {
		if strings.TrimSpace(versions[name]) == "" {
			problems = append(problems, fmt.Errorf("%s.%s must not be empty", group, name))
		}
	}
	return problems
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
