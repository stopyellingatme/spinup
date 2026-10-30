// Package compose renders the Compose files of the sidecar services (ADR 0022 §2):
// compose.services.yml for a release, and .spinup/compose.services.yml for spinup dev.
// The renderers are pure functions of the services, the app, and the environment.
// They never read the clock, random values, or a map in iteration order.
package compose

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/stopyellingatme/spinup/internal/services"
	"go.yaml.in/yaml/v3"
)

// Resources are the resource limits of one service in one environment. An empty field
// means no limit.
type Resources struct {
	// CPUs is the CPU limit, such as "1.5".
	CPUs string
	// Memory is the memory limit, such as "512m".
	Memory string
}

// DeployInput is the input of Deploy.
type DeployInput struct {
	// App is the app name. It names the secrets directory /opt/<app>/secrets.
	App string
	// Services are the declared services, in declaration order.
	Services []services.Service
	// Images maps a service name to its literal image ref, such as todo-ocr:20261030.
	Images map[string]string
	// Monitoring adds the OpenTelemetry variables to each service.
	Monitoring bool
	// Worker adds SERVICE_<NAME>_URL to the worker service, as well as to the api service.
	Worker bool
	// Replicas maps a service name to its replica count. A missing name means 1.
	Replicas map[string]int
	// Resources maps a service name to its limits. A missing name means no limit.
	Resources map[string]Resources
}

// DevInput is the input of Dev.
type DevInput struct {
	// App is the app name. It forms the OpenTelemetry service name. Empty means the bare
	// service name.
	App string
	// Services are the declared services, in declaration order.
	Services []services.Service
	// LGTM sends the OpenTelemetry data of each service to the otel-lgtm container.
	LGTM bool
}

const (
	deployHeader = "Written by spinup deploy from spinup.toml. Do not edit: spinup writes this file again for each release.\n" +
		"The color project uses it as a second -f file after compose.yml, which defines the networks."
	devHeader = "Written by spinup dev from spinup.toml. Do not edit: spinup writes this file again each time it starts.\n" +
		"The paths are relative to the app root, the Compose project directory of spinup dev."
	unpinnedReason = "# spinup:allow unpinned-image spinup deploy sets the release tag, never latest"
)

// Deploy renders compose.services.yml for one release. With no services it returns nil
// bytes and no error. The file defines no networks, because compose.yml defines them
// and the color project reads both files.
func Deploy(in DeployInput) ([]byte, error) {
	if len(in.Services) == 0 {
		return nil, nil
	}
	if in.App == "" {
		return nil, fmt.Errorf("render compose.services.yml: the app name is empty. Try: set name in spinup.toml")
	}
	svcs := newMap()
	for _, s := range in.Services {
		node, err := deployService(in, s)
		if err != nil {
			return nil, err
		}
		svcs.add(s.Name, node)
	}
	for _, name := range []string{"api", "worker"} {
		if name == "worker" && !in.Worker {
			continue
		}
		env := newMap()
		for _, s := range in.Services {
			env.add(services.URLEnv(s.Name), quoted(services.DeployURL(s)))
		}
		svcs.add(name, mapOf("environment", env.node))
	}
	return encode(deployHeader, mapOf("services", svcs.node))
}

func deployService(in DeployInput, s services.Service) (*yaml.Node, error) {
	if err := services.ValidName(s.Name); err != nil {
		return nil, fmt.Errorf("render compose.services.yml: %w", err)
	}
	image := in.Images[s.Name]
	if image == "" {
		return nil, fmt.Errorf("render compose.services.yml: service %q has no image ref. Try: run spinup build --image so that the release has an image for %s", s.Name, s.Name)
	}
	if n, ok := in.Replicas[s.Name]; ok && n < 0 {
		return nil, fmt.Errorf("render compose.services.yml: service %q has %d replicas. Try: set [replicas] %s to 1 or more in deploy/environments/<env>.toml", s.Name, n, s.Name)
	}
	env, err := environment(s, deployExtras(in, s))
	if err != nil {
		return nil, err
	}

	m := newMap()
	imageNode := plain(image)
	imageNode.LineComment = unpinnedReason
	m.add("image", imageNode)
	m.add("user", quoted("${DEPLOY_UID:-1000}:${DEPLOY_GID:-1000}"))
	m.add("read_only", boolean(true))
	m.add("cap_drop", flow(plain("ALL")))
	m.add("security_opt", flow(quoted("no-new-privileges:true")))
	m.add("tmpfs", seq(plain("/tmp")))
	m.add("environment", env)
	if len(s.Secrets) > 0 {
		var mounts []*yaml.Node
		for _, name := range s.Secrets {
			mounts = append(mounts, plain(fmt.Sprintf("/opt/%s/secrets/%s:/run/secrets/%s:ro", in.App, name, name)))
		}
		m.add("volumes", seq(mounts...))
	}
	nets := newMap()
	nets.add("app", mapOf("aliases", seq(plain(services.Alias(s.Name, "${SPINUP_COLOR}")))))
	if s.DB {
		nets.add("db", emptyMap())
	}
	m.add("networks", nets.node)
	if len(s.Healthcheck) > 0 {
		test := []*yaml.Node{quoted("CMD")}
		for _, arg := range s.Healthcheck {
			test = append(test, quoted(arg))
		}
		hc := newMap()
		hc.add("test", flow(test...))
		hc.add("interval", plain("5s"))
		hc.add("timeout", plain("3s"))
		hc.add("retries", integer(3))
		hc.add("start_period", plain("10s"))
		m.add("healthcheck", hc.node)
	}
	m.add("deploy", deploySection(in, s))
	m.add("stop_grace_period", plain("20s"))
	m.add("restart", plain("unless-stopped"))
	m.add("logging", logging())
	return m.node, nil
}

func deploySection(in DeployInput, s services.Service) *yaml.Node {
	d := newMap()
	replicas := 1
	if n, ok := in.Replicas[s.Name]; ok {
		replicas = n
	}
	d.add("replicas", integer(replicas))
	res := newMap()
	if r := in.Resources[s.Name]; r.CPUs != "" || r.Memory != "" {
		limits := newMap()
		if r.CPUs != "" {
			limits.add("cpus", quoted(r.CPUs))
		}
		if r.Memory != "" {
			limits.add("memory", plain(r.Memory))
		}
		res.add("limits", limits.node)
	}
	if s.GPUs != "" {
		device := newMap()
		device.add("driver", plain("nvidia"))
		device.add("capabilities", flow(plain("gpu")))
		if n, err := strconv.Atoi(s.GPUs); err == nil {
			device.add("count", integer(n))
		} else {
			device.add("count", plain(s.GPUs))
		}
		res.add("reservations", mapOf("devices", seq(device.node)))
	}
	if len(res.node.Content) > 0 {
		d.add("resources", res.node)
	}
	return d.node
}

func logging() *yaml.Node {
	opts := newMap()
	opts.add("max-size", quoted("10m"))
	opts.add("max-file", quoted("3"))
	l := newMap()
	l.add("driver", plain("json-file"))
	l.add("options", opts.node)
	return l.node
}

func deployExtras(in DeployInput, s services.Service) [][2]string {
	if !in.Monitoring {
		return nil
	}
	return [][2]string{
		{"OTEL_EXPORTER_OTLP_ENDPOINT", "http://alloy:4318"},
		{"OTEL_SERVICE_NAME", services.OTelName(in.App, s.Name)},
		{"OTEL_RESOURCE_ATTRIBUTES", "service.instance.id=${SPINUP_COLOR}"},
	}
}

// Dev renders .spinup/compose.services.yml for spinup dev. With no services it returns
// nil bytes and no error. The build paths are relative to the app root, because spinup dev
// passes --project-directory <root>.
func Dev(in DevInput) ([]byte, error) {
	if len(in.Services) == 0 {
		return nil, nil
	}
	svcs := newMap()
	for _, s := range in.Services {
		if err := services.ValidName(s.Name); err != nil {
			return nil, fmt.Errorf("render .spinup/compose.services.yml: %w", err)
		}
		var extras [][2]string
		for _, name := range s.Secrets {
			extras = append(extras, [2]string{name, "${" + name + ":-}"})
		}
		if in.LGTM {
			extras = append(extras,
				[2]string{"OTEL_EXPORTER_OTLP_ENDPOINT", "http://otel-lgtm:4318"},
				[2]string{"OTEL_SERVICE_NAME", devOTelName(in.App, s.Name)})
		}
		env, err := environmentDev(s, extras)
		if err != nil {
			return nil, err
		}
		build := newMap()
		build.add("context", plain(services.ContextDir(s.Name)))
		build.add("dockerfile", plain("Dockerfile"))
		m := newMap()
		m.add("build", build.node)
		m.add("ports", seq(quoted(fmt.Sprintf("127.0.0.1:%d:%d", s.DevPort, s.Port))))
		m.add("environment", env)
		svcs.add(s.Name, m.node)
	}
	return encode(devHeader, mapOf("services", svcs.node))
}

// environment builds the environment of a deploy service: PORT, the Env pairs in key
// order, the _FILE variable of each secret, then the extra variables.
func environment(s services.Service, extras [][2]string) (*yaml.Node, error) {
	var files [][2]string
	for _, name := range s.Secrets {
		files = append(files, [2]string{name + "_FILE", "/run/secrets/" + name})
	}
	return buildEnv(s, files, extras)
}

// environmentDev builds the environment of a dev service. The extras hold the secrets
// and the OpenTelemetry variables.
func environmentDev(s services.Service, extras [][2]string) (*yaml.Node, error) {
	return buildEnv(s, nil, extras)
}

func buildEnv(s services.Service, files, extras [][2]string) (*yaml.Node, error) {
	env := newMap()
	seen := map[string]bool{"PORT": true}
	env.add("PORT", quoted(strconv.Itoa(s.Port)))
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if seen[k] {
			return nil, fmt.Errorf("render compose: service %q sets the variable %s twice. Try: remove %s from env in spinup.toml, because spinup sets it", s.Name, k, k)
		}
		seen[k] = true
		// Compose reads $ in a value as an interpolation. A literal value needs $$.
		env.add(k, quoted(strings.ReplaceAll(s.Env[k], "$", "$$")))
	}
	for _, pair := range slices.Concat(files, extras) {
		if seen[pair[0]] {
			return nil, fmt.Errorf("render compose: service %q sets the variable %s twice. Try: remove %s from env in spinup.toml, because spinup sets it", s.Name, pair[0], pair[0])
		}
		seen[pair[0]] = true
		env.add(pair[0], quoted(pair[1]))
	}
	return env.node, nil
}

func encode(header string, root *yaml.Node) ([]byte, error) {
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	var out bytes.Buffer
	for _, line := range strings.Split(header, "\n") {
		out.WriteString("# " + line + "\n")
	}
	out.WriteString("\n")
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("render compose: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("render compose: %w", err)
	}
	return out.Bytes(), nil
}

// ordered is a YAML mapping that keeps the order of its keys.
type ordered struct{ node *yaml.Node }

func newMap() *ordered { return &ordered{node: &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}} }

func (o *ordered) add(key string, value *yaml.Node) {
	o.node.Content = append(o.node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func mapOf(key string, value *yaml.Node) *yaml.Node {
	m := newMap()
	m.add(key, value)
	return m.node
}

func emptyMap() *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
}

// plain is a string that the encoder quotes only when it must.
func plain(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

// quoted is a string that is always in double quotes.
func quoted(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: yaml.DoubleQuotedStyle}
}

func integer(v int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(v)}
}

func boolean(v bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(v)}
}

func seq(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items}
}

func flow(items ...*yaml.Node) *yaml.Node {
	n := seq(items...)
	n.Style = yaml.FlowStyle
	return n
}

// devOTelName returns the OpenTelemetry service name of a service in spinup dev.
func devOTelName(app, name string) string {
	if app == "" {
		return name
	}
	return services.OTelName(app, name)
}
