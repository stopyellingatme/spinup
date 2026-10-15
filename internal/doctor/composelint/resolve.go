package composelint

import "go.yaml.in/yaml/v3"

// resolveNode copies aliases and YAML merges without changing the parsed tree.
// Merge sequences prefer their first mapping. Explicit keys always win.
func resolveNode(node *yaml.Node, active map[*yaml.Node]bool) *yaml.Node {
	if node == nil || active[node] {
		return nil
	}
	active[node] = true
	defer delete(active, node)
	if node.Kind == yaml.AliasNode {
		resolved := resolveNode(node.Alias, active)
		if resolved != nil {
			copy := *resolved
			copy.HeadComment = node.HeadComment
			copy.LineComment = node.LineComment
			copy.FootComment = node.FootComment
			return &copy
		}
		return nil
	}
	copy := *node
	copy.Content = nil
	if node.Kind != yaml.MappingNode {
		for _, child := range node.Content {
			if resolved := resolveNode(child, active); resolved != nil {
				copy.Content = append(copy.Content, resolved)
			}
		}
		return &copy
	}

	seen := make(map[string]bool)
	var merges []*yaml.Node
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Tag == "!!merge" {
			merges = append(merges, value)
			// Keep comments on the service's own merge key for suppressions.
			copy.HeadComment += "\n" + key.HeadComment + "\n" + key.LineComment + "\n" + key.FootComment
			copy.HeadComment += "\n" + value.HeadComment + "\n" + value.LineComment + "\n" + value.FootComment
			continue
		}
		if resolved := resolveNode(value, active); resolved != nil {
			copy.Content = append(copy.Content, key, resolved)
			seen[key.Value] = true
		}
	}
	for _, merge := range merges {
		resolved := resolveNode(merge, active)
		for _, source := range sequenceValues(resolved) {
			if source.Kind != yaml.MappingNode {
				continue
			}
			for i := 0; i+1 < len(source.Content); i += 2 {
				key := source.Content[i]
				if !seen[key.Value] {
					copy.Content = append(copy.Content, key, source.Content[i+1])
					seen[key.Value] = true
				}
			}
		}
	}
	return &copy
}
