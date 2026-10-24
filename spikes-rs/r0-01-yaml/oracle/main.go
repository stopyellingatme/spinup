// Command oracle writes Go YAML fixtures. Run from the repository root.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

type action struct {
	Block bool     `json:"block"`
	Op    string   `json:"op"`
	Path  []string `json:"path"`
	Value string   `json:"value"`
}
type fixture struct {
	ID       string   `json:"id"`
	Family   string   `json:"family"`
	Mode     string   `json:"mode"`
	Input    string   `json:"input"`
	Actions  []action `json:"actions"`
	Comments []string `json:"comments"`
}

func encode(n any, indent int) (string, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(indent)
	if err := enc.Encode(n); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}

func at(n *yaml.Node, path []string) *yaml.Node {
	n = yamlRoot(n)
	for _, key := range path {
		if n == nil {
			return nil
		}
		if n.Kind == yaml.SequenceNode {
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(n.Content) {
				return nil
			}
			n = n.Content[i]
		} else {
			n = YAMLGet(n, key)
		}
	}
	return n
}

func apply(doc *yaml.Node, a action) (bool, error) {
	if a.Op == "noop" {
		return false, nil
	}
	var v yaml.Node
	if a.Op != "delete" {
		if err := yaml.Unmarshal([]byte(a.Value), &v); err != nil {
			return false, err
		}
	}
	if a.Op == "append" {
		n := at(doc, a.Path)
		if n == nil {
			return false, fmt.Errorf("missing sequence: %v", a.Path)
		}
		if n.Tag == "!!null" {
			n.Kind, n.Tag, n.Value, n.Style = yaml.SequenceNode, "!!seq", "", 0
		}
		if a.Block {
			n.Style = 0
		}
		n.Content = append(n.Content, yamlRoot(&v))
		return true, nil
	}
	parent := at(doc, a.Path[:len(a.Path)-1])
	if parent == nil || parent.Kind != yaml.MappingNode {
		return true, YAMLSet(doc, yamlRoot(&v), a.Path...)
	}
	key := a.Path[len(a.Path)-1]
	if a.Op == "delete" {
		return YAMLDelete(parent, key), nil
	}
	return true, YAMLSet(parent, yamlRoot(&v), key)
}

// rotationActions records the exact nodes installed by addRotationTooling.
// The fragments describe edit values. They never contain the output document.
func rotationActions() ([]action, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("write_files: []\nruncmd: []\n"), &doc); err != nil {
		return nil, err
	}
	if err := addRotationTooling(&doc, "demo"); err != nil {
		return nil, err
	}
	var out []action
	for _, key := range []string{"write_files", "runcmd"} {
		for _, n := range YAMLGet(&doc, key).Content {
			value, err := encode(n, 2)
			if err != nil {
				return nil, err
			}
			out = append(out, action{Op: "append", Path: []string{key}, Value: value})
		}
	}
	return out, nil
}

func generate(root string) error {
	data, err := os.ReadFile(filepath.Join(root, "cases.json"))
	if err != nil {
		return err
	}
	var cases []fixture
	if err := json.Unmarshal(data, &cases); err != nil {
		return err
	}
	rotation, err := rotationActions()
	if err != nil {
		return err
	}
	for _, f := range cases {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(f.Input), &doc); err != nil {
			return fmt.Errorf("%s: %w", f.ID, err)
		}
		// Use go-yaml formatting on every input. The noop case tests raw bytes too.
		input, err := encode(&doc, 2)
		if err != nil {
			return err
		}
		if f.Mode == "map" {
			var m map[string]any
			if err := yaml.Unmarshal([]byte(f.Input), &m); err != nil {
				return err
			}
			// Keep comments in the input. The real Compose writer discards them.
			input, err = encode(&doc, 4)
			if err != nil {
				return err
			}
		}
		if f.Family == "rotation" || f.Family == "userdata" {
			f.Actions = append(f.Actions, rotation...)
		}
		var output string
		switch f.Mode {
		case "server":
			var keyText string
			if err := yaml.Unmarshal([]byte(f.Actions[0].Value), &keyText); err != nil {
				return err
			}
			key, err := yaml.Marshal(keyText)
			if err != nil {
				return err
			}
			output = strings.ReplaceAll(input, "- SPINUP_DEPLOY_SSH_KEY", "- "+strings.TrimSpace(string(key)))
		default:
			result, _, err := EditYAML([]byte(input), func(d *yaml.Node) (bool, error) {
				changed := false
				for _, a := range f.Actions {
					c, err := apply(d, a)
					if err != nil {
						return false, err
					}
					changed = changed || c
				}
				return changed, nil
			})
			if err != nil {
				return fmt.Errorf("%s: %w", f.ID, err)
			}
			output = string(result)
			if f.Mode == "map" {
				var m map[string]any
				if err := yaml.Unmarshal(result, &m); err != nil {
					return err
				}
				output, err = encode(m, 4)
				if err != nil {
					return err
				}
			}
		}
		if f.Family == "sops" || f.Family == "alert" || f.Family == "compose" || f.Family == "userdata" || f.Family == "rotation" {
			actual, err := realOutput(f, input)
			if err != nil {
				return fmt.Errorf("%s real edit: %w", f.ID, err)
			}
			if actual != output {
				return fmt.Errorf("%s: fragment edits differ from the production oracle\nFRAG:\n%s\nREAL:\n%s", f.ID, output, actual)
			}
			output = actual
		}
		// This list contains YAML comments, not '#' characters inside shell strings.
		f.Comments = strings.FieldsFunc(yamlComments(&doc), func(r rune) bool { return r == '\n' })
		f.Input = ""
		dir := filepath.Join(root, f.ID)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		meta, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			return err
		}
		for name, data := range map[string][]byte{"input.yaml": []byte(input), "expected.yaml": []byte(output), "edit.json": append(meta, '\n')} {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
				return err
			}
		}
	}
	return nil
}

func main() {
	if err := generate("spikes-rs/r0-01-yaml/fixtures"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
