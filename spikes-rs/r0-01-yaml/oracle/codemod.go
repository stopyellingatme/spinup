// Oracle copy of internal/codemod/yaml.go. The error wrapper is local.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"
)

// EditYAML edits one YAML document and uses two spaces for indentation.
// An unchanged document retains its original bytes.
func EditYAML(src []byte, edit func(*yaml.Node) (bool, error)) ([]byte, bool, error) {
	var doc yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(src))
	if err := decoder.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, false, fmt.Errorf("YAML: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, false, fmt.Errorf("YAML: expected one document")
	}
	before := cloneYAML(&doc)
	changed, err := edit(&doc)
	if err != nil {
		return nil, false, fmt.Errorf("YAML: %w", err)
	}
	if !changed || reflect.DeepEqual(before, &doc) {
		return src, false, nil
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&doc); err != nil {
		return nil, false, fmt.Errorf("YAML: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, false, fmt.Errorf("YAML: %w", err)
	}
	return out.Bytes(), !bytes.Equal(src, out.Bytes()), nil
}

// YAMLGet returns the mapping value at path, or nil when it is absent.
func YAMLGet(doc *yaml.Node, path ...string) *yaml.Node {
	n := yamlRoot(doc)
	for _, key := range path {
		if n == nil || n.Kind != yaml.MappingNode {
			return nil
		}
		i := yamlIndex(n, key)
		if i < 0 {
			return nil
		}
		n = n.Content[i+1]
	}
	return n
}

// YAMLSet sets a mapping value and creates missing mappings.
// It preserves comments and style on an existing value.
func YAMLSet(doc *yaml.Node, value *yaml.Node, path ...string) error {
	if doc == nil || value == nil || len(path) == 0 {
		return fmt.Errorf("a document, value, and nonempty path are required")
	}
	if doc.Kind == 0 {
		doc.Kind = yaml.DocumentNode
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 0 {
		doc.Content = append(doc.Content, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
	}
	n := yamlRoot(doc)
	for i, key := range path {
		if n == nil || n.Kind != yaml.MappingNode {
			return fmt.Errorf("path %q crosses a non-mapping value", path[:i])
		}
		index := yamlIndex(n, key)
		if i == len(path)-1 {
			if index < 0 {
				n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, cloneYAML(value))
			} else {
				old := n.Content[index+1]
				replacement := cloneYAML(value)
				replacement.Line = old.Line
				replacement.Column = old.Column
				if replacement.HeadComment == "" {
					replacement.HeadComment = old.HeadComment
				}
				if replacement.LineComment == "" {
					replacement.LineComment = old.LineComment
				}
				if replacement.FootComment == "" {
					replacement.FootComment = old.FootComment
				}
				if replacement.Style == 0 && replacement.Kind == old.Kind {
					replacement.Style = old.Style
				}
				// Keep comments from removed descendants on the mapping key.
				// A key comment has a stable position when the value becomes a scalar.
				comments := missingYAMLComments(yamlComments(old), yamlComments(replacement))
				keyNode := n.Content[index]
				additions := replacement.HeadComment + "\n" + comments
				keyNode.HeadComment = appendYAMLComments(keyNode.HeadComment, additions)
				replacement.HeadComment = ""
				*old = *replacement
			}
			return nil
		}
		if index < 0 {
			child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
			n = child
		} else {
			child := n.Content[index+1]
			if child.Kind == yaml.ScalarNode && (child.Tag == "!!null" || child.Value == "") {
				child.Kind = yaml.MappingNode
				child.Tag = "!!map"
				child.Value = ""
				child.Content = nil
			}
			n = child
		}
	}
	return nil
}

// YAMLDelete removes a mapping entry and retains its comments in the mapping.
func YAMLDelete(doc *yaml.Node, path ...string) bool {
	if len(path) == 0 {
		return false
	}
	parent := YAMLGet(doc, path[:len(path)-1]...)
	if parent == nil || parent.Kind != yaml.MappingNode {
		return false
	}
	i := yamlIndex(parent, path[len(path)-1])
	if i < 0 {
		return false
	}
	comments := yamlComments(parent.Content[i]) + yamlComments(parent.Content[i+1])
	parent.Content = append(parent.Content[:i], parent.Content[i+2:]...)
	if i < len(parent.Content) {
		parent.Content[i].HeadComment = comments + parent.Content[i].HeadComment
	} else {
		parent.FootComment = comments + parent.FootComment
	}
	return true
}

func yamlRoot(n *yaml.Node) *yaml.Node {
	if n != nil && n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return nil
		}
		return n.Content[0]
	}
	return n
}
func yamlIndex(n *yaml.Node, key string) int {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return i
		}
	}
	return -1
}
func cloneYAML(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	out := *n
	if n.Content != nil {
		out.Content = make([]*yaml.Node, len(n.Content))
		for i, c := range n.Content {
			out.Content[i] = cloneYAML(c)
		}
	}
	return &out
}
func yamlComments(n *yaml.Node) string {
	var out string
	for _, s := range []string{n.HeadComment, n.LineComment, n.FootComment} {
		if s != "" {
			out += s + "\n"
		}
	}
	for _, c := range n.Content {
		out += yamlComments(c)
	}
	return out
}

func missingYAMLComments(old, current string) string {
	seen := map[string]bool{}
	for _, line := range strings.Split(current, "\n") {
		seen[line] = true
	}
	var missing []string
	for _, line := range strings.Split(old, "\n") {
		if line != "" && !seen[line] {
			missing = append(missing, line)
			seen[line] = true
		}
	}
	return strings.Join(missing, "\n")
}
func appendYAMLComments(current, additions string) string {
	missing := missingYAMLComments(additions, current)
	if missing == "" {
		return current
	}
	if current == "" {
		return missing
	}
	return current + "\n" + missing
}
