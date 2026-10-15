package pack

import (
	"os"
	"strings"
	"testing"
)

func TestMetadataValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, edit, want string }{
		{"valid", "", ""},
		{"api", "api = 9", "API"},
		{"range", "spinup = '>=9.0.0'", "range"},
		{"bad-range", "spinup = 'banana'", "range"},
		{"escape", "name = '../escape'", "name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := fixture(t)
			if tc.edit != "" {
				key, _, _ := strings.Cut(tc.edit, " =")
				replaceLine(t, root+"/plugin.toml", key, tc.edit)
			}
			tr, err := readTree(root)
			if err != nil {
				t.Fatal(err)
			}
			_, err = validate(tr, "1.2.3")
			if tc.want == "" && err != nil {
				t.Fatal(err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}

func TestVersionRange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		r, v string
		want bool
	}{
		{">=1.0.0 <2.0.0", "1.2.3", true}, {">=1.0.0, <2.0.0", "2.0.0", false},
		{"^0.2.3", "0.3.0", false}, {"~1.2.3", "1.2.9", true}, {"1.2.3 || >=2.0.0", "2.1.0", true},
		{"*", "0.0.0-dev", true}, {">=1.0.0", "dev", true},
	} {
		ok, err := matchesRange(tc.r, tc.v)
		if err != nil || ok != tc.want {
			t.Errorf("%s %s: %v %v", tc.r, tc.v, ok, err)
		}
	}
}

func TestExamplePluginFixtures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		valid bool
	}{{"example", true}, {"bad-api", false}, {"path-escape", false}, {"protected-path", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr, err := readTree("../../../testdata/plugins/" + tc.name)
			if err != nil {
				t.Fatal(err)
			}
			_, err = validate(tr, "1.2.3")
			if (err == nil) != tc.valid {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestProtectedOutputPaths(t *testing.T) {
	t.Parallel()
	for _, path := range []string{".git/hooks/pre-commit", ".GIT/config", ".spinup/plugins.allow", ".SPINUP/plugins.history", ".github/workflows/ci.yml", ".GitHub/CODEOWNERS", "spinup.lock", "SPINUP.TOML", "api/.GiT/config"} {
		for _, kind := range []string{"output", "rename", "generator"} {
			t.Run(kind+"/"+path, func(t *testing.T) {
				t.Parallel()
				root := fixture(t)
				file := root + "/pack.toml"
				data := "name='example'\n[[files]]\npath=" + tomlString(path) + "\nsource='files/example.txt.tmpl'\nclass='managed'\n"
				switch kind {
				case "rename":
					data = "name='example'\n[[files]]\npath='safe.txt'\nsource='files/example.txt.tmpl'\nclass='managed'\nrenamed_from=" + tomlString(path) + "\n"
				case "generator":
					file = root + "/plugin.toml"
					original, err := os.ReadFile(file)
					if err != nil {
						t.Fatal(err)
					}
					data = string(original) + "\n[[generators]]\nname='evil'\n[[generators.files]]\npath=" + tomlString(path) + "\nsource='files/example.txt.tmpl'\nclass='managed'\n"
				}
				if err := os.WriteFile(file, []byte(data), 0644); err != nil {
					t.Fatal(err)
				}
				tr, err := readTree(root)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := validate(tr, "1.2.3"); err == nil || !strings.Contains(err.Error(), "Try:") {
					t.Fatalf("protected %s accepted: %v", path, err)
				}
			})
		}
	}
}

func TestCheckOutputPath(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path  string
		valid bool
	}{
		{"api/handlers/example.go", true}, {".github-notes.txt", true},
		{"api/spinup.lock", true}, {"api/.spinup/data", true},
		{"api/.gitkeep", true}, {".git", false}, {".SPINUP", false},
		{".GitHub", false}, {"spinup.LOCK", false}, {"spinup.toml", false},
		{"../escape", false}, {"/absolute", false}, {"a//b", false},
		{"a/./b", false}, {"a\\b", false}, {"", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			if err := CheckOutputPath(tc.path); (err == nil) != tc.valid {
				t.Fatalf("CheckOutputPath(%q) = %v", tc.path, err)
			}
		})
	}
	// Metadata samples are safe. The actual argument can select a protected path.
	for _, arg := range []string{".GiT/hooks/pre-commit", ".spinup/plugins.allow", ".github/workflows/ci.yml", "spinup.lock"} {
		path, err := checkTemplatePath("{{.Name}}", map[string]string{"Name": arg})
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckOutputPath(path); err == nil {
			t.Fatalf("rendered argument %q accepted", arg)
		}
	}
}

func TestGeneratorSourceCanUseArguments(t *testing.T) {
	t.Parallel()
	root := fixture(t)
	filename := root + "/plugin.toml"
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("\n[[generators]]\nname = 'copy'\nargs = ['Name']\n[[generators.files]]\npath = 'copies/{{.Name}}.txt'\nsource = 'files/{{.Name}}.txt.tmpl'\nclass = 'scaffold'\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	tr, err := readTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validate(tr, "1.2.3"); err != nil {
		t.Fatal(err)
	}
}
