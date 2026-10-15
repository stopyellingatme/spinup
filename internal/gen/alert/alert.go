// Package alert generates custom Prometheus alert rules.
package alert

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"go.yaml.in/yaml/v3"

	"github.com/stopyellingatme/spinup/internal/codemod"
	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

const customRulesPath = "monitoring/alerts/custom.yml"

var alertName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func init() {
	registry.Register(gen.Generator{Name: "alert", Short: "Add a custom Prometheus alert rule", Usage: "<name> --expr <PromQL>", Flags: flags, Run: plan})
}

func flags(fs *pflag.FlagSet) {
	fs.String("expr", "", "PromQL expression for the alert")
	fs.String("for", "5m", "time that the expression must stay true")
	fs.String("severity", "warning", "alert severity: warning or critical")
	fs.String("summary", "", "short alert summary")
}

func plan(ctx context.Context, in gen.Input) (*gen.Output, error) {
	if len(in.Args) != 1 {
		return nil, fmt.Errorf("expected one alert name\nTry: spinup g alert HighErrors --expr 'sum(rate(http_server_request_duration_seconds_count{http_response_status_code=~\"5..\"}[5m])) > 0'")
	}
	if in.Lock == nil || !slices.Contains(in.Lock.Packs, "monitoring") {
		return nil, fmt.Errorf("the app has no monitoring pack\nTry: create the app with --modules ...,monitoring")
	}
	name := in.Args[0]
	if !alertName.MatchString(name) {
		return nil, fmt.Errorf("invalid alert name %q: use letters, digits, and underscores\nTry: spinup g alert HighErrors --expr 'up == 0'", name)
	}
	rule, err := inputRule(in.Flags)
	if err != nil {
		return nil, err
	}
	mappingValue(rule, "alert").Value = name
	path := filepath.Join(in.Root, filepath.FromSlash(customRulesPath))
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w\nTry: create the app with --modules ...,monitoring", customRulesPath, err)
	}
	// editErr keeps this generator's own errors out of the YAML editor's generic wrapping.
	var editErr error
	data, _, err := codemod.EditYAML(src, func(doc *yaml.Node) (bool, error) {
		groups, err := ensureGroups(doc)
		if err != nil {
			editErr = err
			return false, err
		}
		if hasAlert(groups, name) {
			editErr = fmt.Errorf("alert %q already exists\nTry: choose another alert name", name)
			return false, editErr
		}
		group := customGroup(groups)
		rules := mappingValue(group, "rules")
		if rules == nil || rules.Kind != yaml.SequenceNode {
			editErr = fmt.Errorf("custom group has invalid rules\nTry: repair %s", customRulesPath)
			return false, editErr
		}
		rules.Style = 0
		rules.Content = append(rules.Content, rule)
		return true, nil
	})
	if editErr != nil {
		return nil, editErr
	}
	if err != nil {
		return nil, fmt.Errorf("edit %s: %w", customRulesPath, err)
	}
	out := &gen.Output{
		Plan:  vfs.Plan{Files: []vfs.File{{Path: customRulesPath, Data: data, Class: string(manifest.Seed)}}},
		Next:  []string{"spinup monitor alerts"},
		Edits: []string{customRulesPath},
	}
	if tool, err := exec.LookPath("promtool"); err == nil {
		if err := validateRules(ctx, tool, data); err != nil {
			return nil, err
		}
		out.Commands = []gen.Command{{Dir: ".", Argv: []string{"promtool", "check", "rules", customRulesPath}}}
	} else {
		out.Next = append(out.Next, "promtool is unavailable; skipped alert validation")
	}
	return out, nil
}

func inputRule(fs *pflag.FlagSet) (*yaml.Node, error) {
	if fs == nil {
		return nil, fmt.Errorf("--expr is required\nTry: spinup g alert HighErrors --expr 'up == 0'")
	}
	expr, _ := fs.GetString("expr")
	if strings.TrimSpace(expr) == "" {
		return nil, fmt.Errorf("--expr is required\nTry: spinup g alert HighErrors --expr 'up == 0'")
	}
	forValue, _ := fs.GetString("for")
	severity, _ := fs.GetString("severity")
	summary, _ := fs.GetString("summary")
	if !validDuration(forValue) {
		return nil, fmt.Errorf("invalid --for duration %q: use a Prometheus duration\nTry: pass --for 5m", forValue)
	}
	if severity != "warning" && severity != "critical" {
		return nil, fmt.Errorf("invalid severity %q: use warning or critical\nTry: pass --severity warning", severity)
	}
	if strings.TrimSpace(summary) == "" {
		summary = nameSummary(expr)
	}
	return mapping(map[string]*yaml.Node{
		"alert": scalar(""), "expr": scalar(expr), "for": scalar(forValue),
		"labels":      mapping(map[string]*yaml.Node{"severity": scalar(severity)}),
		"annotations": mapping(map[string]*yaml.Node{"summary": scalar(summary)}),
	}), nil
}

// Prometheus durations use integer units, from largest to smallest, at most once.
var durationPattern = regexp.MustCompile(`^(?:([0-9]+)y)?(?:([0-9]+)w)?(?:([0-9]+)d)?(?:([0-9]+)h)?(?:([0-9]+)m)?(?:([0-9]+)s)?(?:([0-9]+)ms)?$`)

func validDuration(value string) bool {
	if value == "0" {
		return true
	}
	parts := durationPattern.FindStringSubmatch(value)
	if value == "" || parts == nil {
		return false
	}
	units := [...]time.Duration{365 * 24 * time.Hour, 7 * 24 * time.Hour, 24 * time.Hour, time.Hour, time.Minute, time.Second, time.Millisecond}
	var total uint64
	for i, part := range parts[1:] {
		if part == "" {
			continue
		}
		n, err := strconv.ParseUint(part, 10, 64)
		unit := uint64(units[i])
		if err != nil || n > (math.MaxInt64-total)/unit {
			return false
		}
		total += n * unit
	}
	return true
}

func validateRules(ctx context.Context, tool string, data []byte) error {
	dir, err := os.MkdirTemp("", "spinup-alert-*")
	if err != nil {
		return fmt.Errorf("create temporary alert validation directory: %w\nTry: check the temporary directory permissions", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "custom.yml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write temporary alert rules: %w\nTry: check the temporary directory permissions", err)
	}
	if _, err := (exec.OSRunner{}).Run(ctx, exec.Cmd{Name: tool, Args: []string{"check", "rules", path}, Timeout: 30 * time.Second}); err != nil {
		return fmt.Errorf("validate %s before writing: %w\nTry: correct --expr, --for, or the existing alert rules", customRulesPath, err)
	}
	return nil
}

func ensureGroups(doc *yaml.Node) (*yaml.Node, error) {
	groups := codemod.YAMLGet(doc, "groups")
	if groups == nil {
		groups = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		if err := codemod.YAMLSet(doc, groups, "groups"); err != nil {
			return nil, err
		}
	}
	if groups.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("groups must be a YAML sequence")
	}
	return groups, nil
}

func customGroup(groups *yaml.Node) *yaml.Node {
	for _, group := range groups.Content {
		if mappingValue(group, "name") != nil && mappingValue(group, "name").Value == "custom" {
			return group
		}
	}
	group := mapping(map[string]*yaml.Node{
		"name":  scalar("custom"),
		"rules": {Kind: yaml.SequenceNode, Tag: "!!seq"},
	})
	groups.Content = append(groups.Content, group)
	return group
}

func hasAlert(groups *yaml.Node, name string) bool {
	for _, group := range groups.Content {
		if rules := mappingValue(group, "rules"); rules != nil && rules.Kind == yaml.SequenceNode {
			for _, rule := range rules.Content {
				if alert := mappingValue(rule, "alert"); alert != nil && alert.Value == name {
					return true
				}
			}
		}
	}
	return false
}

func mapping(values map[string]*yaml.Node) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, key := range []string{"alert", "expr", "for", "labels", "annotations", "name", "rules", "severity", "summary"} {
		if value, ok := values[key]; ok {
			node.Content = append(node.Content, scalar(key), value)
		}
	}
	return node
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func nameSummary(expr string) string { return "Alert expression is true: " + expr }
