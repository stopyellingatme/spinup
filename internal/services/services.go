// Package services holds the sidecar service contract of ADR 0022: the [[services]]
// table of spinup.toml, its rules, and the names derived from a service name.
//
// Contract file from the services pre-seed. Main loop only: units do not edit it.
// The package is a leaf. It imports nothing from spinup, so project, build, deploy,
// devloop, and the generators can all use it.
package services

import (
	"errors"
	"fmt"
	"go/token"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Dir is the directory of all services, relative to the project root.
const Dir = "services"

// Service is one [[services]] table of spinup.toml.
type Service struct {
	// Name is the service name, such as "ocr".
	Name string `toml:"name"`
	// Port is the container port that the service listens on.
	Port int `toml:"port"`
	// DevPort is the host port of the service in `spinup dev`.
	DevPort int `toml:"dev_port"`
	// Health is an HTTP path that answers 200 when the service is ready.
	Health string `toml:"health"`
	// OpenAPI is the OpenAPI file, relative to services/<name>/. Empty means no Go client.
	OpenAPI string `toml:"openapi"`
	// Healthcheck is the Compose health check command in exec form. Empty means none.
	Healthcheck []string `toml:"healthcheck"`
	// Secrets are the secret names that the service can read in /run/secrets.
	Secrets []string `toml:"secrets"`
	// Env holds literal, non-secret environment variables.
	Env map[string]string `toml:"env"`
	// DB joins the service to the db network.
	DB bool `toml:"db"`
	// GPUs is "" (none), "all", or a count such as "1".
	GPUs string `toml:"gpus"`
	// Public is a URL path prefix, such as "/svc/ocr", that the edge (Caddy) routes to the
	// service in the live color. The edge strips the prefix. Empty means internal only
	// (ADR 0023).
	Public string `toml:"public"`
	// Auth is the edge check of a public route: "" or AuthNone (the service checks callers
	// itself), or AuthSession (the edge asks the API for a valid user session first).
	Auth string `toml:"auth"`
}

// The values of Service.Auth.
const (
	// AuthNone sends every request of a public route to the service.
	AuthNone = "none"
	// AuthSession sends a request of a public route to the service only when the API
	// accepts its session cookie (GET /api/session answers 2xx). It needs the auth module.
	AuthSession = "session"
)

// ReservedPublicPrefixes are the URL paths that the app itself serves. A public prefix
// cannot equal one of them or sit under one.
var ReservedPublicPrefixes = []string{"/api", "/_app", "/healthz", "/readyz", "/version", "/.well-known"}

// IsPublic reports whether the service has a public route.
func (s Service) IsPublic() bool { return s.Public != "" }

// AuthMode returns the edge check of the public route: AuthNone or AuthSession.
func (s Service) AuthMode() string {
	if s.Auth == "" {
		return AuthNone
	}
	return s.Auth
}

// Reserved holds the names that a service cannot use, because spinup uses them for its
// own services, projects, or networks.
var Reserved = []string{
	"alloy", "api", "backup", "backups", "caddy", "edge", "grafana", "loki", "mailpit",
	"migrate", "mimir", "monitoring", "postgres", "pyroscope", "tempo", "web", "worker",
}

// DefaultDevPorts are the host ports of `spinup dev` that a service cannot use:
// the API, the Vite server, PostgreSQL, Mailpit, and the LGTM ports.
var DefaultDevPorts = []int{8080, 5173, 5432, 1025, 8025, 3000, 4317, 4318}

var (
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9]{0,19}$`)
	varPattern  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	segPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
)

// ValidName reports whether name is a valid, unreserved service name. A Go keyword
// is not valid, because the name is the package name of the generated Go client.
func ValidName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("service name %q must match %s. Try: use lower-case letters and digits, such as ocr", name, namePattern)
	}
	if slices.Contains(Reserved, name) {
		return fmt.Errorf("service name %q is reserved for spinup. Try: choose another name, such as %ssvc", name, name)
	}
	// The name is the Go package name of the generated client (api/internal/services/<name>).
	if token.IsKeyword(name) {
		return fmt.Errorf("service name %q is a Go keyword, and the Go client uses the name as its package name. Try: choose another name, such as %ssvc", name, name)
	}
	return nil
}

// Validate checks a list of services. reservedDevPorts are the dev ports in use by the
// app itself; nil means DefaultDevPorts. Each problem ends with a Try: line.
func Validate(list []Service, reservedDevPorts []int) error {
	if reservedDevPorts == nil {
		reservedDevPorts = DefaultDevPorts
	}
	var problems []error
	names := map[string]bool{}
	devPorts := map[int]string{}
	for _, s := range list {
		label := fmt.Sprintf("service %q", s.Name)
		if err := ValidName(s.Name); err != nil {
			problems = append(problems, err)
		}
		if names[s.Name] {
			problems = append(problems, fmt.Errorf("%s is declared twice in spinup.toml. Try: remove one [[services]] table", label))
		}
		names[s.Name] = true
		if s.Port < 1 || s.Port > 65535 {
			problems = append(problems, fmt.Errorf("%s: port %d must be from 1 to 65535. Try: set port = 8080", label, s.Port))
		}
		switch {
		case s.DevPort < 1 || s.DevPort > 65535:
			problems = append(problems, fmt.Errorf("%s: dev_port %d must be from 1 to 65535. Try: set dev_port = 8101", label, s.DevPort))
		case slices.Contains(reservedDevPorts, s.DevPort):
			problems = append(problems, fmt.Errorf("%s: dev_port %d is used by the app in spinup dev. Try: choose a port from 8101", label, s.DevPort))
		case devPorts[s.DevPort] != "":
			problems = append(problems, fmt.Errorf("%s: dev_port %d is also used by service %q. Try: give each service its own dev_port", label, s.DevPort, devPorts[s.DevPort]))
		default:
			devPorts[s.DevPort] = s.Name
		}
		if !strings.HasPrefix(s.Health, "/") {
			problems = append(problems, fmt.Errorf("%s: health %q must be an HTTP path that starts with /. Try: set health = \"/healthz\"", label, s.Health))
		}
		if s.OpenAPI != "" && !localPath(s.OpenAPI) {
			problems = append(problems, fmt.Errorf("%s: openapi %q must be a relative path inside services/%s. Try: set openapi = \"openapi.yaml\"", label, s.OpenAPI, s.Name))
		}
		for _, name := range s.Secrets {
			if !varPattern.MatchString(name) {
				problems = append(problems, fmt.Errorf("%s: secret %q must match %s. Try: use the name from spinup secrets", label, name, varPattern))
			}
		}
		for _, key := range sortedKeys(s.Env) {
			if !varPattern.MatchString(key) || strings.HasPrefix(key, "SPINUP_") || strings.HasPrefix(key, "OTEL_") {
				problems = append(problems, fmt.Errorf("%s: env key %q must match %s and cannot start with SPINUP_ or OTEL_. Try: rename the variable", label, key, varPattern))
			}
		}
		if s.GPUs != "" && s.GPUs != "all" {
			if n, err := strconv.Atoi(s.GPUs); err != nil || n < 1 {
				problems = append(problems, fmt.Errorf("%s: gpus %q must be \"\", \"all\", or a count of 1 or more. Try: set gpus = \"all\"", label, s.GPUs))
			}
		}
		problems = append(problems, publicProblems(label, s)...)
	}
	problems = append(problems, prefixOverlaps(list)...)
	return errors.Join(problems...)
}

// publicProblems checks the public and auth fields of one service.
func publicProblems(label string, s Service) []error {
	var problems []error
	switch s.Auth {
	case "", AuthNone, AuthSession:
	default:
		problems = append(problems, fmt.Errorf("%s: auth %q must be \"none\" or \"session\". Try: set auth = \"session\" or remove it", label, s.Auth))
	}
	if s.Public == "" {
		if s.Auth != "" {
			problems = append(problems, fmt.Errorf("%s: auth is set, but the service has no public route. Try: set public = \"/svc/%s\" or remove auth", label, s.Name))
		}
		return problems
	}
	try := fmt.Sprintf("Try: set public = \"/svc/%s\"", s.Name)
	if !strings.HasPrefix(s.Public, "/") || s.Public == "/" || strings.HasSuffix(s.Public, "/") {
		return append(problems, fmt.Errorf("%s: public %q must be a path that starts with / and does not end with /, and it cannot be /. %s", label, s.Public, try))
	}
	for _, seg := range strings.Split(s.Public[1:], "/") {
		if !segPattern.MatchString(seg) {
			return append(problems, fmt.Errorf("%s: public %q has the segment %q, which must match %s. %s", label, s.Public, seg, segPattern, try))
		}
	}
	for _, r := range ReservedPublicPrefixes {
		if underPrefix(s.Public, r) {
			problems = append(problems, fmt.Errorf("%s: public %q is under %s, which the app serves itself. %s", label, s.Public, r, try))
		}
	}
	return problems
}

// prefixOverlaps reports public prefixes that are equal or nested between services.
func prefixOverlaps(list []Service) []error {
	var problems []error
	for i, a := range list {
		for _, b := range list[i+1:] {
			if a.Public == "" || b.Public == "" {
				continue
			}
			if underPrefix(a.Public, b.Public) || underPrefix(b.Public, a.Public) {
				problems = append(problems, fmt.Errorf("service %q: public %q overlaps public %q of service %q. Try: give each service its own prefix, such as /svc/<name>", a.Name, a.Public, b.Public, b.Name))
			}
		}
	}
	return problems
}

// underPrefix reports whether path equals prefix or sits under it.
func underPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// EnvPrefix returns SERVICE_<NAME>, the prefix of the variables of a service.
func EnvPrefix(name string) string { return "SERVICE_" + strings.ToUpper(name) }

// URLEnv returns SERVICE_<NAME>_URL, the variable that holds the base URL of a service.
func URLEnv(name string) string { return EnvPrefix(name) + "_URL" }

// DeployURL returns the base URL of a service inside a color project. The color stays
// a Compose variable, because rollback changes only SPINUP_COLOR in .env.
func DeployURL(s Service) string {
	return fmt.Sprintf("http://%s:%d", Alias(s.Name, "${SPINUP_COLOR}"), s.Port)
}

// DevURL returns the base URL of a service in `spinup dev`.
func DevURL(s Service) string { return fmt.Sprintf("http://127.0.0.1:%d", s.DevPort) }

// Alias returns the network alias of a service in a color, such as ocr-blue.
func Alias(name, color string) string { return name + "-" + color }

// ImageName returns the image name of a service without a tag, such as todo-ocr.
func ImageName(app, name string) string { return app + "-" + name }

// OTelName returns the OpenTelemetry service name of a service, such as todo-ocr.
func OTelName(app, name string) string { return app + "-" + name }

// ContextDir returns the Docker build context of a service, relative to the project root.
func ContextDir(name string) string { return Dir + "/" + name }

// Dockerfile returns the Dockerfile of a service, relative to the project root.
func Dockerfile(name string) string { return ContextDir(name) + "/Dockerfile" }

// ClientDir returns the Go package directory of the generated client, relative to the
// project root.
func ClientDir(name string) string { return "api/internal/services/" + name }

func localPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
