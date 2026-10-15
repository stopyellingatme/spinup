package pack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, data := range map[string]string{
		"plugin.toml":            "name = 'example'\nversion = '1.0.0'\napi = 1\nspinup = '>=0.0.0'\ndescription = 'Example pack'\n",
		"pack.toml":              "name = 'example'\n[[files]]\npath = 'example.txt'\nsource = 'files/example.txt.tmpl'\nclass = 'managed'\n",
		"files/example.txt.tmpl": "hello\n",
	} {
		filename := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func replaceLine(t *testing.T, path, key, value string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, key+" =") {
			lines[i] = value
		}
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatal(err)
	}
}
