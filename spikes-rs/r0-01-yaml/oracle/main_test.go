package main

import (
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestCommittedOracleOutputs(t *testing.T) {
	root := t.TempDir()
	cases, err := os.ReadFile("../fixtures/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cases.json"), cases, 0600); err != nil {
		t.Fatal(err)
	}
	if err := generate(root); err != nil {
		t.Fatal(err)
	}
	dirs, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		count++
		for _, name := range []string{"input.yaml", "edit.json", "expected.yaml"} {
			actual, err := os.ReadFile(filepath.Join(root, d.Name(), name))
			if err != nil {
				t.Fatal(err)
			}
			expected, err := os.ReadFile(filepath.Join("../fixtures", d.Name(), name))
			if err != nil {
				t.Fatal(err)
			}
			if string(actual) != string(expected) {
				t.Errorf("%s/%s differs from the Go oracle", d.Name(), name)
			}
		}
	}
	if count < 25 {
		t.Fatalf("only %d fixtures", count)
	}
}

func TestUnchangedKeepsBytes(t *testing.T) {
	source := []byte("a:    b # Keep whitespace\n")
	result, changed, err := EditYAML(source, func(*yaml.Node) (bool, error) { return false, nil })
	if err != nil || changed || string(result) != string(source) {
		t.Fatalf("unchanged edit: %t %v %q", changed, err, result)
	}
}
