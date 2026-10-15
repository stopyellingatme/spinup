package composelint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/stopyellingatme/spinup/internal/doctor"
	"go.yaml.in/yaml/v3"
)

var interpolation = regexp.MustCompile(`^(?:\$[A-Za-z_][A-Za-z0-9_]*|\$\{[^}]+\})$`)
var secretName = regexp.MustCompile(`(?i)(PASSWORD|SECRET|TOKEN|API_KEY|PRIVATE_KEY)`)
var suppression = regexp.MustCompile(`(?m)spinup:allow\s+(\S+)(?:\s+([^\r\n]+?))?\s*$`)

// Finding identifies one Compose security problem.
type Finding struct {
	File    string
	Service string
	Rule    string
	Message string
	Try     string
}

// Lint checks Compose YAML data for unsafe service settings.
func Lint(file string, data []byte) ([]Finding, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	root := documentNode(&document)
	services := mappingValue(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil, nil
	}

	findings := make([]Finding, 0)
	for index := 0; index+1 < len(services.Content); index += 2 {
		serviceKey, service := services.Content[index], services.Content[index+1]
		if service.Kind != yaml.MappingNode {
			continue
		}
		serviceFindings := lintService(file, serviceKey.Value, serviceKey, service)
		findings = append(findings, serviceFindings...)
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Service != findings[j].Service {
			return findings[i].Service < findings[j].Service
		}
		if findings[i].Rule != findings[j].Rule {
			return findings[i].Rule < findings[j].Rule
		}
		return findings[i].Message < findings[j].Message
	})
	return findings, nil
}

func lintService(file, name string, serviceKey, service *yaml.Node) []Finding {
	comments := nodeComments(serviceKey)
	for _, node := range service.Content {
		comments = append(comments, nodeComments(node)...)
	}
	allow, bad := parseSuppressions(comments)
	findings := make([]Finding, 0)
	for range bad {
		findings = append(findings, finding(file, name, "bad-suppression", "The suppression has no reason.", "Add a reason after the suppression rule."))
	}
	add := func(rule, message, try string, nodes ...*yaml.Node) {
		if allow[rule] || suppressed(rule, nodes...) {
			return
		}
		findings = append(findings, finding(file, name, rule, message, try))
	}

	if node := mappingValue(service, "privileged"); node != nil && isTrue(node) {
		add("privileged", "The service uses privileged mode.", "Remove privileged mode.", node)
	}
	if node := mappingValue(service, "volumes"); node != nil {
		for _, volume := range sequenceValues(node) {
			if dockerSocket(volume) {
				add("docker-socket", "The service mounts the Docker socket.", "Remove the Docker socket mount.", volume)
			}
		}
	}
	for _, key := range []string{"network_mode", "pid", "ipc"} {
		if node := mappingValue(service, key); node != nil && strings.EqualFold(strings.TrimSpace(node.Value), "host") {
			add("host-namespace", "The service shares a host namespace.", "Use an isolated namespace.", node)
		}
	}
	if node := mappingValue(service, "cap_add"); node != nil {
		for _, capability := range sequenceValues(node) {
			value := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(capability.Value)), "CAP_")
			if value == "ALL" || value == "SYS_ADMIN" {
				add("dangerous-cap", "The service adds a dangerous Linux capability.", "Remove the dangerous capability.", capability)
			}
		}
	}
	if node := mappingValue(service, "security_opt"); node != nil {
		for _, option := range sequenceValues(node) {
			value := strings.ToLower(strings.TrimSpace(option.Value))
			if value == "seccomp:unconfined" || value == "seccomp=unconfined" || value == "apparmor:unconfined" || value == "apparmor=unconfined" {
				add("unconfined", "The service disables a security profile.", "Use the default security profile.", option)
			}
		}
	}
	if node := mappingValue(service, "ports"); node != nil && !publicAllowed(service) {
		for _, port := range sequenceValues(node) {
			if publicPort(port) {
				add("public-port", "The service publishes a public port.", "Bind the port to localhost or add spinup.public: \"true\".", port)
			}
		}
	}
	if image := mappingValue(service, "image"); image != nil && mappingValue(service, "build") == nil && !strings.Contains(image.Value, "@sha256:") {
		add("unpinned-image", "The service image is not pinned by digest.", "Pin the image with an @sha256 digest.", image)
	}
	if environment := mappingValue(service, "environment"); environment != nil {
		for _, entry := range environmentValues(environment) {
			if secretName.MatchString(entry.key) && !strings.HasSuffix(strings.ToUpper(entry.key), "_FILE") && entry.value != "" && !interpolation.MatchString(entry.value) {
				add("literal-secret", "The service sets a literal secret value.", "Use an environment interpolation or a _FILE variable.", entry.node)
			}
		}
	}
	return findings
}

func finding(file, service, rule, message, try string) Finding {
	return Finding{File: file, Service: service, Rule: rule, Message: message, Try: try}
}

func documentNode(node *yaml.Node) *yaml.Node {
	if node != nil && node.Kind == yaml.DocumentNode && len(node.Content) != 0 {
		return node.Content[0]
	}
	return node
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}

func sequenceValues(node *yaml.Node) []*yaml.Node {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.SequenceNode {
		return node.Content
	}
	return []*yaml.Node{node}
}

func isTrue(node *yaml.Node) bool {
	return node.Tag == "!!bool" && node.Value == "true"
}

func dockerSocket(node *yaml.Node) bool {
	if node.Kind == yaml.MappingNode {
		node = mappingValue(node, "source")
		if node == nil {
			return false
		}
		return socketPath(node.Value)
	}
	return socketPath(strings.SplitN(node.Value, ":", 2)[0])
}

func socketPath(value string) bool {
	return value == "/var/run/docker.sock" || value == "/run/docker.sock"
}

func publicAllowed(service *yaml.Node) bool {
	labels := mappingValue(service, "labels")
	if labels == nil {
		return false
	}
	if labels.Kind == yaml.MappingNode {
		value := mappingValue(labels, "spinup.public")
		return value != nil && strings.EqualFold(value.Value, "true")
	}
	for _, label := range sequenceValues(labels) {
		if label.Value == "spinup.public=true" {
			return true
		}
	}
	return false
}

func publicPort(node *yaml.Node) bool {
	if node.Kind == yaml.MappingNode {
		hostIP := mappingValue(node, "host_ip")
		return hostIP == nil || isPublicHost(hostIP.Value)
	}
	value := strings.TrimSpace(node.Value)
	if !strings.Contains(value, ":") {
		return true
	}
	if strings.HasPrefix(value, "[") {
		end := strings.Index(value, "]")
		return end < 0 || isPublicHost(value[1:end])
	}
	parts := strings.Split(value, ":")
	if len(parts) == 2 {
		return true
	}
	return isPublicHost(parts[0])
}

func isPublicHost(host string) bool {
	host = strings.TrimSpace(host)
	return host == "" || host == "0.0.0.0" || host == "::"
}

type environmentValue struct {
	key   string
	value string
	node  *yaml.Node
}

func environmentValues(node *yaml.Node) []environmentValue {
	values := make([]environmentValue, 0)
	switch node.Kind {
	case yaml.MappingNode:
		for index := 0; index+1 < len(node.Content); index += 2 {
			values = append(values, environmentValue{key: node.Content[index].Value, value: node.Content[index+1].Value, node: node.Content[index+1]})
		}
	case yaml.SequenceNode:
		for _, entry := range node.Content {
			key, value, found := strings.Cut(entry.Value, "=")
			if found {
				values = append(values, environmentValue{key: key, value: value, node: entry})
			}
		}
	}
	return values
}

func nodeComments(node *yaml.Node) []string {
	if node == nil {
		return nil
	}
	return []string{node.HeadComment, node.LineComment, node.FootComment}
}

func parseSuppressions(comments []string) (map[string]bool, []string) {
	allowed := make(map[string]bool)
	bad := make([]string, 0)
	for _, comment := range comments {
		matches := suppression.FindAllStringSubmatch(comment, -1)
		for _, match := range matches {
			if strings.TrimSpace(match[2]) == "" {
				bad = append(bad, match[1])
				continue
			}
			allowed[match[1]] = true
		}
	}
	return allowed, bad
}

func suppressed(rule string, nodes ...*yaml.Node) bool {
	for _, node := range nodes {
		allowed, _ := parseSuppressions(nodeComments(node))
		if allowed[rule] {
			return true
		}
	}
	return false
}

func init() {
	doctor.Register(doctor.Check{
		ID:    "compose-security",
		Title: "Compose security lint",
		Scope: doctor.Local,
		Quick: true,
		Run:   runCheck,
	})
}

func runCheck(_ context.Context, c *doctor.Context) doctor.Result {
	patterns := []string{"compose*.yml", "compose*.yaml"}
	files := make([]string, 0)
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(c.Root, "deploy", pattern))
		if err != nil {
			return doctor.Result{Status: doctor.Fail, Message: fmt.Sprintf("failed to find Compose files: %v", err), Try: "Check the project path."}
		}
		files = append(files, matches...)
	}
	sort.Strings(files)
	if len(files) == 0 {
		return doctor.Result{Status: doctor.Skip, Message: "No Compose files found."}
	}
	findings := make([]Finding, 0)
	for _, path := range files {
		data, err := os.ReadFile(path)
		relative, relativeErr := filepath.Rel(c.Root, path)
		if relativeErr != nil {
			relative = path
		}
		relative = filepath.ToSlash(relative)
		if err != nil {
			return doctor.Result{Status: doctor.Fail, Message: fmt.Sprintf("failed to read %s: %v", relative, err), Try: "Check the Compose file permissions."}
		}
		fileFindings, err := Lint(relative, data)
		if err != nil {
			return doctor.Result{Status: doctor.Fail, Message: fmt.Sprintf("failed to parse %s: %v", relative, err), Try: "Fix the Compose YAML syntax."}
		}
		findings = append(findings, fileFindings...)
	}
	if len(findings) == 0 {
		return doctor.Result{Status: doctor.OK, Message: "Compose security lint passed."}
	}
	lines := make([]string, 0, min(len(findings), 10)+1)
	for _, item := range findings[:min(len(findings), 10)] {
		lines = append(lines, fmt.Sprintf("%s: %s: %s: %s", item.File, item.Service, item.Rule, item.Message))
	}
	if more := len(findings) - 10; more > 0 {
		lines = append(lines, fmt.Sprintf("%d more findings.", more))
	}
	return doctor.Result{Status: doctor.Fail, Message: strings.Join(lines, "\n"), Try: findings[0].Try}
}
