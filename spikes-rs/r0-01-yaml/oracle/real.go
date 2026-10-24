// These oracle routines copy production edits. They use no internal imports.
package main

import (
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

func creationRule(source []byte, env string, recipients []string) ([]byte, error) {
	data, _, err := EditYAML(source, func(doc *yaml.Node) (bool, error) {
		rules := YAMLGet(doc, "creation_rules")
		if rules == nil {
			if err := YAMLSet(doc, &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}, "creation_rules"); err != nil {
				return false, err
			}
			rules = YAMLGet(doc, "creation_rules")
		}
		if rules.Kind != yaml.SequenceNode {
			return false, errors.New("creation_rules must be a sequence")
		}
		pattern := `^secrets/` + env + `\.enc\.yaml$`
		var rule *yaml.Node
		for _, entry := range rules.Content {
			path := YAMLGet(entry, "path_regex")
			if path != nil && path.Value == pattern {
				if rule != nil {
					return false, errors.New("duplicate environment rules")
				}
				rule = entry
			}
		}
		if rule == nil {
			rule = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			if err := YAMLSet(rule, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: pattern}, "path_regex"); err != nil {
				return false, err
			}
			rules.Content = append(rules.Content, rule)
		}
		if rules.LineComment != "" {
			rules.HeadComment = strings.TrimSpace(rules.HeadComment + "\n" + rules.LineComment)
			rules.LineComment = ""
		}
		rules.Style = 0
		return true, YAMLSet(rule, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: strings.Join(recipients, ",")}, "age")
	})
	if err != nil {
		return nil, fmt.Errorf("creation rules: %w", err)
	}
	return data, nil
}

func ensureGroups(doc *yaml.Node) (*yaml.Node, error) {
	groups := YAMLGet(doc, "groups")
	if groups == nil {
		groups = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		if err := YAMLSet(doc, groups, "groups"); err != nil {
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

func realOutput(f fixture, input string) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(input), &doc); err != nil {
		return "", err
	}
	switch f.Family {
	case "sops":
		var age string
		a := f.Actions[0]
		if a.Op == "append" {
			var rule yaml.Node
			if err := yaml.Unmarshal([]byte(a.Value), &rule); err != nil {
				return "", err
			}
			age = YAMLGet(&rule, "age").Value
		} else {
			if err := yaml.Unmarshal([]byte(a.Value), &age); err != nil {
				return "", err
			}
		}
		output, err := creationRule([]byte(input), "prod", strings.Split(age, ","))
		return string(output), err
	case "alert":
		var rule yaml.Node
		if err := yaml.Unmarshal([]byte(f.Actions[len(f.Actions)-1].Value), &rule); err != nil {
			return "", err
		}
		output, _, err := EditYAML([]byte(input), func(d *yaml.Node) (bool, error) {
			groups, err := ensureGroups(d)
			if err != nil {
				return false, err
			}
			if hasAlert(groups, "HighErrors") {
				return false, fmt.Errorf("duplicate alert")
			}
			group := customGroup(groups)
			rules := mappingValue(group, "rules")
			rules.Style = 0
			rules.Content = append(rules.Content, yamlRoot(&rule))
			return true, nil
		})
		return string(output), err
	case "userdata", "rotation":
		if f.Family == "userdata" {
			var keys yaml.Node
			if err := yaml.Unmarshal([]byte(f.Actions[0].Value), &keys); err != nil {
				return "", err
			}
			// These inputs have no ssh_keys entry. setKey appends the new mapping.
			doc.Content[0].Content = append(doc.Content[0].Content, scalar("ssh_keys"), yamlRoot(&keys))
		}
		if err := addRotationTooling(&doc, "demo"); err != nil {
			return "", err
		}
		return encode(&doc, 2)
	case "compose":
		var m map[string]any
		if err := yaml.Unmarshal([]byte(input), &m); err != nil {
			return "", err
		}
		services := m["services"].(map[string]any)
		pg := services["postgres"].(map[string]any)
		pg["image"] = "demo-postgres:18-pgbackrest2.57"
		if len(f.Actions) > 1 {
			pg["environment"].(map[string]any)["PGDATA"] = "/var/lib/postgresql/18/docker"
			for i, mount := range pg["volumes"].([]any) {
				if text, ok := mount.(string); ok && strings.HasPrefix(text, "postgres_data:") {
					pg["volumes"].([]any)[i] = "postgres_data:/var/lib/postgresql"
				}
				if item, ok := mount.(map[string]any); ok && item["source"] == "postgres_data" {
					item["target"] = "/var/lib/postgresql"
				}
			}
			m["volumes"].(map[string]any)["postgres_data"] = map[string]any{"external": true, "name": "demo_pgdata_18"}
		}
		return encode(m, 4)
	default:
		return "", fmt.Errorf("no production oracle for %s", f.Family)
	}
}
