package pack

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	execx "github.com/stopyellingatme/spinup/internal/exec"
)

func TestPluginQuestionNeedsDefault(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"add", "update", "verify"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			m := manager(t)
			source := fixture(t)
			if operation != "add" {
				if err := m.Add(t.Context(), "path:"+source, ""); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(source, "pack.toml")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, []byte("\n[[questions]]\nkey='word'\ntype='string'\n")...)
			if err := os.WriteFile(path, data, 0644); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "add":
				err = m.Add(t.Context(), "path:"+source, "")
			case "update":
				err = m.Update(t.Context(), "example", "")
			case "verify":
				err = m.Verify(t.Context())
			}
			if err == nil || !strings.Contains(err.Error(), "default") || !strings.Contains(err.Error(), "Try:") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestPluginRecordsIgnoredWarning(t *testing.T) {
	t.Parallel()
	for _, ignored := range []bool{true, false} {
		name := "tracked"
		if ignored {
			name = "ignored"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := manager(t)
			runner := execx.OSRunner{}
			if _, err := runner.Run(t.Context(), execx.Cmd{Name: "git", Args: []string{"init"}, Dir: m.Root}); err != nil {
				t.Fatal(err)
			}
			rules := ".spinup/*\n!.spinup/plugins.allow\n!.spinup/plugins.history\n"
			if ignored {
				rules = ".spinup/\n"
			}
			if err := os.WriteFile(filepath.Join(m.Root, ".gitignore"), []byte(rules), 0644); err != nil {
				t.Fatal(err)
			}
			for _, operation := range []string{"add", "update"} {
				m.Out = &bytes.Buffer{}
				var err error
				if operation == "add" {
					err = m.Add(t.Context(), "path:"+fixture(t), "")
				} else {
					err = m.Update(t.Context(), "example", "")
				}
				if err != nil {
					t.Fatal(err)
				}
				output := m.Out.(*bytes.Buffer).String()
				for _, want := range []string{"Try:", ".spinup/*", "!.spinup/plugins.allow", "!.spinup/plugins.history"} {
					if strings.Contains(output, want) != ignored {
						t.Fatalf("%s ignored=%t output=%s", operation, ignored, output)
					}
				}
			}
		})
	}
}

func TestRemoveDescribesNextUpgrade(t *testing.T) {
	t.Parallel()
	m := manager(t)
	if err := m.Add(t.Context(), "path:"+fixture(t), ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(t.Context(), "example"); err != nil {
		t.Fatal(err)
	}
	output := m.Out.(*bytes.Buffer).String()
	for _, want := range []string{"next upgrade", "unmodified", "orphans"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q: %s", want, output)
		}
	}
	if strings.Contains(output, "Retained files:\n") {
		t.Fatal(output)
	}
}

func TestPluginReservedGeneratorSource(t *testing.T) {
	t.Parallel()
	m := manager(t)
	source := fixture(t)
	if err := os.MkdirAll(filepath.Join(source, "@gen"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "@gen/file.txt"), []byte("content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(source, "pack.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.ReplaceAll(string(data), "files/example.txt.tmpl", "@gen/file.txt"))
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := m.Add(t.Context(), "path:"+source, ""); err == nil || !strings.Contains(err.Error(), "reserved @gen") {
		t.Fatalf("error=%v", err)
	}
}
