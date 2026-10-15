package codemod

import (
	"bytes"
	"os"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestYAML(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("testdata/example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	same, changed, err := EditYAML(src, func(*yaml.Node) (bool, error) { return false, nil })
	if err != nil || changed || !bytes.Equal(src, same) {
		t.Fatal("round trip", err)
	}
	for _, tt := range []struct {
		name string
		edit func(*yaml.Node) (bool, error)
	}{
		{"set", func(doc *yaml.Node) (bool, error) {
			return true, YAMLSet(doc, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "new"}, "app", "name")
		}},
		{"insert", func(doc *yaml.Node) (bool, error) {
			return true, YAMLSet(doc, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "value"}, "new", "child")
		}},
		{"delete", func(doc *yaml.Node) (bool, error) { return YAMLDelete(doc, "app", "items"), nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, changed, err := EditYAML(src, tt.edit)
			if err != nil || !changed {
				t.Fatal(changed, err)
			}
			for _, comment := range []string{"File comment.", "Mapping comment.", "Name comment.", "List comment.", "Item comment.", "Other comment.", "End comment."} {
				if !bytes.Contains(out, []byte(comment)) {
					t.Fatalf("lost %s:\n%s", comment, out)
				}
			}
			again, changed, err := EditYAML(out, tt.edit)
			if err != nil || changed || !bytes.Equal(again, out) {
				t.Fatalf("not idempotent: %v %v\n%s\n%s", changed, err, out, again)
			}
		})
	}
}
func TestYAMLEdges(t *testing.T) {
	t.Parallel()
	out, changed, err := EditYAML(nil, func(doc *yaml.Node) (bool, error) {
		return true, YAMLSet(doc, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "v"}, "a", "b")
	})
	if err != nil || !changed {
		t.Fatal(string(out), changed, err)
	}
	if _, _, err := EditYAML([]byte("a: 1\n---\nb: 2"), func(*yaml.Node) (bool, error) { return false, nil }); err == nil {
		t.Fatal("accepted multiple documents")
	}
	if _, _, err := EditYAML([]byte("a: ["), func(*yaml.Node) (bool, error) { return false, nil }); err == nil {
		t.Fatal("accepted invalid YAML")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("a: 1"), &doc); err != nil {
		t.Fatal(err)
	}
	if err := YAMLSet(&doc, &yaml.Node{}, "a", "b"); err == nil {
		t.Fatal("crossed scalar")
	}
	if YAMLGet(&doc, "missing") != nil || YAMLDelete(&doc, "missing") || YAMLDelete(&doc) {
		t.Fatal("missing path")
	}
	if err := YAMLSet(nil, nil); err == nil {
		t.Fatal("accepted nil")
	}
}

func TestYAMLReplaceComments(t *testing.T) {
	t.Parallel()
	src := []byte("# Header.\napp:\n  # Name.\n  name: old # Inline.\nother: true\n")
	edit := func(doc *yaml.Node) (bool, error) {
		return true, YAMLSet(doc, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "new", HeadComment: "# Added."}, "app")
	}
	out, changed, err := EditYAML(src, edit)
	if err != nil || !changed {
		t.Fatal(err)
	}
	for _, comment := range []string{"# Header.", "# Name.", "# Inline.", "# Added."} {
		if !bytes.Contains(out, []byte(comment)) {
			t.Fatalf("lost %s:\n%s", comment, out)
		}
	}
	again, changed, err := EditYAML(out, edit)
	if err != nil || changed || !bytes.Equal(out, again) {
		t.Fatal("not idempotent", err, string(again))
	}
}
