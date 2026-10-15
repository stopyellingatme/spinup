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
	}{{"example", true}, {"bad-api", false}, {"path-escape", false}} {
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
